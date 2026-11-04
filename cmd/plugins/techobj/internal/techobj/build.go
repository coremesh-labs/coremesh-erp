package techobj

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// --- Anlagen aus den Immobilien aufbauen (seit 0.6.1) ----------------------------------
//
// Alles sind Anlagen, nur auf unterschiedlichen Ebenen: Haus › Wohnung › Zimmer.
// Die Arten der Ebenen stehen im Katalog Anlagenarten (Ebene beim Aufbau), was an
// den Raum gehört, an der Sparte (raumbezogen). Messpunkte kommen über ihre
// Verknüpfung „misst für“ (Rollen mit Kennzeichen Nutzer) an Haus oder Wohnung;
// raumbezogene an das Zimmer aus dem Raumbuch – Zimmer gibt es nur, wo ein
// solcher Messpunkt hängt. Wiederholbar: Vorhandenes wird gefunden, nicht doppelt.

var trailingNo = regexp.MustCompile(`\s+\d+$`)

type builder struct {
	m                     *Module
	ctx                   context.Context
	cc, day               string
	levels                map[string]string // BUILDING|RENTOBJ|ROOM → Anlagenart
	plants, points, rooms int
	unresolved            [][]any
}

func (m *Module) buildAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Company  string `json:"bd_company"`
			Building string `json:"bd_building"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, bld := strings.TrimSpace(in.Data.Company), strings.TrimSpace(in.Data.Building)
	if cc == "" || bld == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Gebäude angeben")
	}
	if err := checkWrite("TechPlant")(ctx, "create", crud.Record{"company_code": cc}); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.setupCatalogs(ctx, cc); err != nil {
		return sdk.Response{}, err
	}
	b := &builder{m: m, ctx: ctx, cc: cc, day: crud.KeyDate(ctx), levels: map[string]string{}}
	res, err := m.db.Query(ctx, "SELECT build_level, code FROM techobj__plant_type WHERE company_code = ? AND is_active = ? AND build_level IS NOT NULL ORDER BY sort_order, code", cc, true)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range res.Rows {
		if _, ok := b.levels[crud.Str(r[0])]; !ok {
			b.levels[crud.Str(r[0])] = crud.Str(r[1])
		}
	}
	for _, l := range []string{"BUILDING", "RENTOBJ", "ROOM"} {
		if b.levels[l] == "" {
			return sdk.Response{}, crud.Invalid("Keine Anlagenart mit Ebene %s – Einstellungen → Anlagenarten", l)
		}
	}
	house, err := b.house(bld)
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Anlage %s aufgebaut: %d Anlagen neu, %d Messpunkte zugeordnet, %d Zimmer", house, b.plants, b.points, b.rooms)
	out := map[string]any{"message": msg, "plant_id": house}
	if len(b.unresolved) > 0 {
		out["message"] = msg + fmt.Sprintf("; %d raumbezogene Messpunkte ohne passenden Raum an der Wohnung", len(b.unresolved))
		out["table"] = map[string]any{"columns": []string{"Messpunkt", "Bezeichnung", "Lage im Raum", "Wohnung"}, "rows": b.unresolved}
	}
	return sdk.Response{Payload: out}, nil
}

// get / list über das Modul Immobilien.
func (b *builder) list(object string, query map[string]any) ([]map[string]any, error) {
	resp, err := b.m.services.Call(b.ctx, object, "list", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &l); err != nil {
		return nil, err
	}
	return l.Items, nil
}

func (b *builder) create(object string, data map[string]any) (map[string]any, error) {
	resp, err := b.m.set.Entity(object).Create(b.ctx, map[string]any{"data": data})
	if err != nil {
		return nil, err
	}
	var rec map[string]any
	if err := sdk.Decode(resp.Payload, &rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// linkCode: Schlüssel im Katalog Verknüpfbare Objekte für ein Object des anderen Moduls.
func (b *builder) linkCode(object string) (string, error) {
	c, err := b.m.one(b.ctx, "SELECT code FROM techobj__link_object WHERE company_code = ? AND service_object = ? AND is_active = ? ORDER BY code LIMIT 1", b.cc, object, true)
	if err == nil && c == "" {
		err = crud.Invalid("%s ist nicht verknüpfbar – Einstellungen → Verknüpfbare Objekte", object)
	}
	return c, err
}

// users: Messpunkte mit heute gültiger Verknüpfung „misst für“ auf das Objekt (mit Beginn).
func (b *builder) users(typ, id string) ([][2]string, error) {
	res, err := b.m.db.Query(b.ctx, `SELECT l.source_id, MIN(l.valid_from) FROM techobj__link l
		JOIN techobj__link_role r ON r.company_code = l.company_code AND r.code = l.role_code AND r.is_user = ?
		WHERE l.company_code = ? AND l.source_kind = 'MeterPoint' AND l.object_type = ? AND l.object_id = ? AND l.valid_to >= ?
		GROUP BY l.source_id ORDER BY l.source_id`, true, b.cc, typ, id, b.day)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, r := range res.Rows {
		out = append(out, [2]string{crud.Str(r[0]), date(r[1])})
	}
	return out, nil
}

// linkedPlant: Anlage, die mit dem Objekt verknüpft ist („misst für“) – sonst neu mit Verknüpfung.
func (b *builder) linkedPlant(typ, id, designation, parent, from string) (string, error) {
	p, err := b.m.one(b.ctx, `SELECT l.source_id FROM techobj__link l JOIN techobj__link_role r ON r.company_code = l.company_code AND r.code = l.role_code AND r.is_user = ?
		WHERE l.company_code = ? AND l.source_kind = 'TechPlant' AND l.object_type = ? AND l.object_id = ? ORDER BY l.valid_from LIMIT 1`, true, b.cc, typ, id)
	if err != nil || p != "" {
		return p, err
	}
	level := "RENTOBJ"
	if parent == "" {
		level = "BUILDING"
	}
	rec, err := b.create("TechPlant", map[string]any{"company_code": b.cc, "designation": designation, "plant_type": b.levels[level],
		"parent_id": nilIfEmpty(parent), "valid_from": from})
	if err != nil {
		return "", err
	}
	b.plants++
	plant := crud.Str(rec["plant_id"])
	role, err := b.m.userRole(b.ctx, b.cc, typ)
	if err != nil {
		return "", err
	}
	if _, err := b.create("TechLink", map[string]any{"company_code": b.cc, "source_kind": "TechPlant", "source_id": plant, "role_code": role,
		"object_type": typ, "object_id": id, "valid_from": from}); err != nil {
		return "", err
	}
	return plant, nil
}

// attach: Messpunkt an die Anlage (ab Beginn seiner Verknüpfung), wenn er dort noch fehlt.
func (b *builder) attach(plant, point, from string) error {
	if v, err := b.m.one(b.ctx, "SELECT 1 FROM techobj__plant_point WHERE company_code = ? AND plant_id = ? AND point_id = ? AND valid_to >= ?", b.cc, plant, point, b.day); err != nil || v != "" {
		return err
	}
	if _, err := b.create("TechPlantPoint", map[string]any{"company_code": b.cc, "plant_id": plant, "point_id": point, "valid_from": from}); err != nil {
		return err
	}
	b.points++
	return nil
}

func earliest(pts [][2]string, fallback string) string {
	out := fallback
	for _, p := range pts {
		if out == "" || p[1] < out {
			out = p[1]
		}
	}
	return out
}

func (b *builder) house(bld string) (string, error) {
	resp, err := b.m.services.Call(b.ctx, "Building", "get", map[string]any{"id": b.cc + "|" + bld})
	if err != nil {
		return "", crud.Invalid("Gebäude %s: %v", bld, err)
	}
	var building map[string]any
	if err := sdk.Decode(resp.Payload, &building); err != nil {
		return "", err
	}
	bType, err := b.linkCode("Building")
	if err != nil {
		return "", err
	}
	rType, err := b.linkCode("RentObject")
	if err != nil {
		return "", err
	}
	hp, err := b.users(bType, bld)
	if err != nil {
		return "", err
	}
	objs, err := b.list("RentObject", map[string]any{"company_code": b.cc, "building_id": bld, "limit": 1000})
	if err != nil {
		return "", err
	}
	type flat struct {
		id, name string
		pts      [][2]string
	}
	var flats []flat
	from := earliest(hp, "")
	for _, o := range objs {
		id := crud.Str(o["object_id"])
		pts, err := b.users(rType, id)
		if err != nil {
			return "", err
		}
		if len(pts) == 0 {
			continue // ohne Messpunkte keine Wohnungs-Ebene
		}
		flats = append(flats, flat{id, crud.Str(o["designation"]), pts})
		from = earliest(pts, from)
	}
	if from == "" {
		return "", crud.Invalid("Gebäude %s: weder am Haus noch an seinen Mietobjekten Messpunkte mit „misst für“", bld)
	}
	house, err := b.linkedPlant(bType, bld, crud.Str(building["designation"]), "", from)
	if err != nil {
		return "", err
	}
	for _, p := range hp {
		if err := b.attach(house, p[0], p[1]); err != nil {
			return "", err
		}
	}
	for _, f := range flats {
		fp, err := b.linkedPlant(rType, f.id, f.name, house, earliest(f.pts, ""))
		if err != nil {
			return "", err
		}
		if err := b.flat(fp, f.id, f.pts); err != nil {
			return "", err
		}
	}
	return house, nil
}

// flat: wohnungsweite Messpunkte an die Wohnung, raumbezogene an ihr Zimmer.
func (b *builder) flat(plant, object string, pts [][2]string) error {
	var rooms []map[string]any
	loaded := false
	for _, p := range pts {
		perRoom, err := b.m.one(b.ctx, `SELECT md.per_room FROM techobj__point pt JOIN techobj__medium md ON md.company_code = pt.company_code AND md.code = pt.medium
			WHERE pt.company_code = ? AND pt.point_id = ?`, b.cc, p[0])
		if err != nil {
			return err
		}
		if !crud.AsBool(perRoom) {
			if err := b.attach(plant, p[0], p[1]); err != nil {
				return err
			}
			continue
		}
		if !loaded {
			if rooms, err = b.list("Room", map[string]any{"company_code": b.cc, "object_id": object, "limit": 1000}); err != nil {
				return err
			}
			loaded = true
		}
		_, pos, err := b.m.pointLocationAt(b.ctx, b.cc, p[0], b.day)
		if err != nil {
			return err
		}
		room := matchRoom(rooms, pos)
		if room == nil {
			name, _ := b.m.one(b.ctx, "SELECT designation FROM techobj__point WHERE company_code = ? AND point_id = ?", b.cc, p[0])
			b.unresolved = append(b.unresolved, []any{p[0], name, pos, object})
			if err := b.attach(plant, p[0], p[1]); err != nil {
				return err
			}
			continue
		}
		rp, err := b.roomPlant(plant, room, p[1])
		if err != nil {
			return err
		}
		if err := b.attach(rp, p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// matchRoom: Raum des Raumbuchs zur Angabe „Lage im Raum“ des Messpunkts –
// gleiche Bezeichnung, sonst ohne laufende Nummer („Wohnzimmer 2“ → Wohnzimmer).
func matchRoom(rooms []map[string]any, pos string) map[string]any {
	pos = strings.TrimSpace(pos)
	if pos == "" {
		return nil
	}
	for _, try := range []string{pos, trailingNo.ReplaceAllString(pos, "")} {
		var hit map[string]any
		n := 0
		for _, r := range rooms {
			if strings.EqualFold(strings.TrimSpace(crud.Str(r["designation"])), try) {
				hit, n = r, n+1
			}
		}
		if n == 1 {
			return hit
		}
	}
	return nil
}

// roomPlant: Zimmer-Anlage unter der Wohnung zum Raum (Raum-Nr.), sonst neu.
func (b *builder) roomPlant(parent string, room map[string]any, from string) (string, error) {
	no := crud.Str(room["room_no"])
	p, err := b.m.one(b.ctx, "SELECT plant_id FROM techobj__plant WHERE company_code = ? AND parent_id = ? AND room_no = ?", b.cc, parent, no)
	if err != nil || p != "" {
		return p, err
	}
	rec, err := b.create("TechPlant", map[string]any{"company_code": b.cc, "designation": crud.Str(room["designation"]), "plant_type": b.levels["ROOM"],
		"parent_id": parent, "room_no": no, "room_type": room["room_type"], "valid_from": from})
	if err != nil {
		return "", err
	}
	b.plants++
	b.rooms++
	return crud.Str(rec["plant_id"]), nil
}
