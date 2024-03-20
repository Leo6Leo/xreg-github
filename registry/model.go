package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	log "github.com/duglin/dlog"
)

var RegexpPropName = regexp.MustCompile("^[a-z_][a-z0-9_./]{0,62}$")
var RegexpMapKey = regexp.MustCompile("^[a-z0-9][a-z0-9_.\\-]{0,62}$")

type ModelSerializer func(*Model, string) ([]byte, error)

var ModelSerializers = map[string]ModelSerializer{}

func IsValidAttributeName(name string) bool {
	return RegexpPropName.MatchString(name)
}

func IsValidMapKey(key string) bool {
	return RegexpMapKey.MatchString(key)
}

type Model struct {
	Registry   *Registry              `json:"-"`
	Schemas    []string               `json:"schemas,omitempty"`
	Attributes Attributes             `json:"attributes,omitempty"`
	Groups     map[string]*GroupModel `json:"groups,omitempty"` // Plural
}

type Attributes map[string]*Attribute // AttrName->Attr

// Defined a separate struct instead of just inlining these attributes so
// that we can just copy them over in one statement in SetSpecPropsFields()
// and so that if we add more we don't need to remember to update that func
type AttrInternals struct {
	levels     string // show only for these levels, ""==all
	immutable  bool   // can change after set?
	dontStore  bool   // don't store this prop in the DB
	httpHeader string // custom HTTP header name, not xRegistry-xxx

	getFn    func(*Entity, *RequestInfo) any // return prop's value
	checkFn  func(*Entity) error             // validate incoming prop
	updateFn func(*Entity, bool) error       // prep prop for saving to DB
}

// Do not include "omitempty" on any attribute that has a default value that
// doesn't match golang's default value for that type. E.g. bool defaults to
// 'false', but Strict needs to default to 'true'. See the custome Unmarshal
// funcs in model.go for how we set those
type Attribute struct {
	Registry       *Registry `json:"-"`
	Name           string    `json:"name,omitempty"`
	Type           string    `json:"type,omitempty"`
	Description    string    `json:"description,omitempty"`
	Enum           []any     `json:"enum,omitempty"` // just scalars though
	Strict         *bool     `json:"strict,omitempty"`
	ReadOnly       bool      `json:"readonly,omitempty"`
	ClientRequired bool      `json:"clientrequired,omitempty"`
	ServerRequired bool      `json:"serverrequired,omitempty"`
	Default        any       `json:"default,omitempty"`

	Attributes Attributes `json:"attributes,omitempty"` // for Objs
	Item       *Item      `json:"item,omitempty"`       // for maps & arrays
	IfValues   IfValues   `json:"ifValues,omitempty"`   // Value

	// Internal fields
	// We have them here so we can have access to them in any func that
	// gets passed the model attribute.
	// If anything gets added below MAKE SURE to update SetSpecPropsFields too
	internals AttrInternals
}

type Item struct { // for maps and arrays
	Registry   *Registry  `json:"-"`
	Type       string     `json:"type,omitempty"`
	Attributes Attributes `json:"attributes,omitempty"` // when 'type'=obj
	Item       *Item      `json:"item,omitempty"`       // when 'type'=map,array
}

type IfValues map[string]*IfValue

type IfValue struct {
	SiblingAttributes Attributes `json:"siblingAttributes,omitempty"`
}

type GroupModel struct {
	SID      string    `json:"-"`
	Registry *Registry `json:"-"`

	Plural     string     `json:"plural"`
	Singular   string     `json:"singular"`
	Attributes Attributes `json:"attributes,omitempty"`

	Resources map[string]*ResourceModel `json:"resources,omitempty"` // Plural
}

type ResourceModel struct {
	SID        string      `json:"-"`
	GroupModel *GroupModel `json:"-"`

	Plural      string     `json:"plural"`
	Singular    string     `json:"singular"`
	Versions    int        `json:"versions"`    // do not include omitempty
	VersionId   bool       `json:"versionid"`   // do not include omitempty
	Latest      bool       `json:"latest"`      // do not include omitempty
	HasDocument bool       `json:"hasdocument"` // do not include omitempty
	Attributes  Attributes `json:"attributes,omitempty"`
}

// To be picky, let's Marshal the list of attributes with Spec defined ones
// first, and then the extensions in alphabetical order
func (attrs Attributes) MarshalJSON() ([]byte, error) {
	buf := bytes.Buffer{}
	attrsCopy := maps.Clone(attrs) // Copy so we can delete keys
	count := 0

	buf.WriteString("{")
	for _, specProp := range OrderedSpecProps {
		if attr, ok := attrsCopy[specProp.Name]; ok {
			delete(attrsCopy, specProp.Name)
			// We need to exclude "model" because we don't want to show the
			// end user "model" as a valid attribute in the model.
			if specProp.Name == "model" {
				continue
			}

			if count > 0 {
				buf.WriteRune(',')
			}
			buf.WriteString(`"` + specProp.Name + `": `)
			tmpBuf, err := json.Marshal(attr)
			if err != nil {
				return nil, err
			}
			buf.Write(tmpBuf)
			count++
		}
	}

	for _, name := range SortedKeys(attrsCopy) {
		if count > 0 {
			buf.WriteRune(',')
		}
		attr := attrsCopy[name]
		buf.WriteString(`"` + name + `": `)
		tmpBuf, err := json.Marshal(attr)
		if err != nil {
			return nil, err
		}
		buf.Write(tmpBuf)
		// delete(attrsCopy, name)
		count++
	}

	buf.WriteString("}")
	return buf.Bytes(), nil
}

func (r *ResourceModel) UnmarshalJSON(data []byte) error {
	// Set the default values
	r.Versions = VERSIONS
	r.VersionId = VERSIONID
	r.Latest = LATEST
	r.HasDocument = HASDOCUMENT

	type tmpResourceModel ResourceModel
	return Unmarshal(data, (*tmpResourceModel)(r))
}

func (m *Model) AddSchema(schema string) error {
	err := Do(m.Registry.tx,
		`INSERT INTO "Schemas" (RegistrySID, "Schema") VALUES(?,?)`,
		m.Registry.DbSID, schema)
	if err != nil {
		err = fmt.Errorf("Error inserting schema(%s): %s", schema, err)
		log.Print(err)
		return err
	}

	for _, s := range m.Schemas {
		if s == schema {
			// already there
			return nil
		}
	}

	m.Schemas = append(m.Schemas, schema)
	sort.Strings(m.Schemas)
	return nil
}

func (m *Model) DelSchema(schema string) error {
	err := Do(m.Registry.tx,
		`DELETE FROM "Schemas" WHERE RegistrySID=? AND "Schema"=?`,
		m.Registry.DbSID, schema)
	if err != nil {
		err = fmt.Errorf("Error deleting schema(%s): %s", schema, err)
		log.Print(err)
		return err
	}

	for i, s := range m.Schemas {
		if s == schema {
			m.Schemas = append(m.Schemas[:i], m.Schemas[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *Model) SetPointers() {
	PanicIf(m.Registry == nil, "Model.Registry can't be nil")

	for _, attr := range m.Attributes {
		attr.SetRegistry(m.Registry)
	}

	for _, gm := range m.Groups {
		gm.SetRegistry(m.Registry)
	}
}

// Save() should be called by these funcs automatically but there may be
// cases where someone would need to call it manually (e.g. setting an
// attribute's property - we should technically find a way to catch those
// cases so code above this shouldn't need to think about it
func (m *Model) Save() error {
	if err := m.Verify(); err != nil {
		// Kind of extreme, but if there's an error revert the entire
		// model to the last known good state. So, all of the changes
		// people made will be lost and any variables are bogus
		// NOTE any local variable pointing to a model entity will need to
		// be refresh/refound, the existing pointer will be bad
		*m = *LoadModel(m.Registry)
		return err
	}

	err := m.SetSchemas(m.Schemas)
	if err != nil {
		return err
	}

	// Create a temporary type so that we don't use the MarshalJSON func
	// in model.go. That one will exclude "model" from the serialization and
	// we don't want to do that when we're saving it in the DB. We only want
	// to do that when we're serializing the model for the end user.
	type tmpAttributes Attributes
	buf, _ := json.Marshal((tmpAttributes)(m.Attributes))
	attrs := string(buf)

	err = DoZeroOne(m.Registry.tx,
		`UPDATE Registries SET Attributes=? WHERE SID=?`,
		attrs, m.Registry.DbSID)
	if err != nil {
		log.Printf("Error updating model: %s", err)
		return err
	}

	for _, gm := range m.Groups {
		if err := gm.Save(); err != nil {
			return err
		}
	}

	return nil
}

func (m *Model) SetSchemas(schemas []string) error {
	err := Do(m.Registry.tx,
		`DELETE FROM "Schemas" WHERE RegistrySID=?`, m.Registry.DbSID)
	if err != nil {
		err = fmt.Errorf("Error deleting schemas: %s", err)
		log.Print(err)
		return err
	}
	m.Schemas = nil

	for _, s := range schemas {
		err = m.AddSchema(s)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Model) AddAttr(name, daType string) (*Attribute, error) {
	return m.AddAttribute(&Attribute{Name: name, Type: daType})
}

func (m *Model) AddAttrMap(name string, item *Item) (*Attribute, error) {
	return m.AddAttribute(&Attribute{Name: name, Type: MAP, Item: item})
}

func (m *Model) AddAttrObj(name string) (*Attribute, error) {
	return m.AddAttribute(&Attribute{Name: name, Type: OBJECT})
}

func (m *Model) AddAttrArray(name string, item *Item) (*Attribute, error) {
	return m.AddAttribute(&Attribute{Name: name, Type: ARRAY, Item: item})
}

func (m *Model) AddAttribute(attr *Attribute) (*Attribute, error) {
	if attr == nil {
		return nil, nil
	}

	if attr.Name != "*" && !IsValidAttributeName(attr.Name) {
		return nil, fmt.Errorf("Invalid attribute name: %s", attr.Name)
	}

	if m.Attributes == nil {
		m.Attributes = Attributes{}
	}

	if attr.Registry == nil {
		attr.Registry = m.Registry
	}

	m.Attributes[attr.Name] = attr

	attr.Item.SetRegistry(m.Registry)

	if err := m.Save(); err != nil {
		return nil, err
	}

	return attr, nil
}

func (m *Model) DelAttribute(name string) error {
	if m.Attributes == nil {
		return nil
	}

	delete(m.Attributes, name)

	return m.Save()
}

func (m *Model) AddGroupModel(plural string, singular string) (*GroupModel, error) {
	if plural == "" {
		return nil, fmt.Errorf("Can't add a GroupModel with an empty plural name")
	}
	if singular == "" {
		return nil, fmt.Errorf("Can't add a GroupModel with an empty sigular name")
	}

	if !IsValidAttributeName(plural) {
		return nil, fmt.Errorf("GroupModel plural name is not valid")
	}

	if !IsValidAttributeName(singular) {
		return nil, fmt.Errorf("GroupModel singular name is not valid")
	}

	for _, gm := range m.Groups {
		if gm.Plural == plural {
			return nil, fmt.Errorf("GroupModel plural %q already exists",
				plural)
		}
		if gm.Singular == singular {
			return nil, fmt.Errorf("GroupModel singular %q already exists",
				singular)
		}
	}

	mSID := NewUUID()
	err := DoOne(m.Registry.tx, `
        INSERT INTO ModelEntities(
            SID, RegistrySID, ParentSID, Plural, Singular, Versions)
        VALUES(?,?,?,?,?,?) `,
		mSID, m.Registry.DbSID, nil, plural, singular, 0)
	if err != nil {
		log.Printf("Error inserting groupModel(%s): %s", plural, err)
		return nil, err
	}
	gm := &GroupModel{
		SID:      mSID,
		Registry: m.Registry,
		Singular: singular,
		Plural:   plural,

		Resources: map[string]*ResourceModel{},
	}

	m.Groups[plural] = gm

	if err = m.Save(); err != nil {
		return nil, err
	}

	return gm, nil
}

func NewItem() *Item {
	return &Item{}
}
func NewItemType(daType string) *Item {
	return &Item{
		Type: daType,
	}
}

func NewItemObject() *Item {
	return &Item{
		Type: OBJECT,
	}
}

func NewItemMap(item *Item) *Item {
	return &Item{
		Type: MAP,
		Item: item,
	}
}

func NewItemArray(item *Item) *Item {
	return &Item{
		Type: ARRAY,
		Item: item,
	}
}

func (i *Item) SetRegistry(reg *Registry) {
	if i == nil {
		return
	}

	i.Registry = reg
	i.Attributes.SetRegistry(reg)
}

func (i *Item) SetItem(item *Item) error {
	i.Item = item
	item.SetRegistry(i.Registry)

	if i.Registry != nil {
		return i.Registry.Model.Save()
	}
	return nil
}

func (i *Item) AddAttr(name, daType string) (*Attribute, error) {
	return i.AddAttribute(&Attribute{Name: name, Type: daType})
}

func (i *Item) AddAttrMap(name string, item *Item) (*Attribute, error) {
	return i.AddAttribute(&Attribute{Name: name, Type: MAP, Item: item})
}

func (i *Item) AddAttrObj(name string) (*Attribute, error) {
	return i.AddAttribute(&Attribute{Name: name, Type: OBJECT})
}

func (i *Item) AddAttrArray(name string, item *Item) (*Attribute, error) {
	return i.AddAttribute(&Attribute{Name: name, Type: ARRAY, Item: item})
}

func (i *Item) AddAttribute(attr *Attribute) (*Attribute, error) {
	if attr == nil {
		return nil, nil
	}

	if attr.Name != "*" && !IsValidAttributeName(attr.Name) {
		return nil, fmt.Errorf("Invalid attribute name: %s", attr.Name)
	}

	if i.Attributes == nil {
		i.Attributes = Attributes{}
	}

	i.Attributes[attr.Name] = attr

	if attr.Registry == nil {
		attr.Registry = i.Registry
	}
	attr.Item.SetRegistry(i.Registry)

	if i.Registry != nil {
		if err := i.Registry.Model.Save(); err != nil {
			return nil, err
		}
	}

	return attr, nil
}

func (i *Item) DelAttribute(name string) error {
	if i.Attributes == nil {
		return nil
	}

	delete(i.Attributes, name)

	if i.Registry != nil {
		return i.Registry.Model.Save()
	}
	return nil
}

func LoadModel(reg *Registry) *Model {
	log.VPrintf(3, ">Enter: LoadModel")
	defer log.VPrintf(3, "<Exit: LoadModel")

	PanicIf(reg == nil, "nil")
	groups := map[string]*GroupModel{} // Model SID -> *GroupModel

	model := &Model{
		Registry: reg,
		Groups:   map[string]*GroupModel{},
	}

	// Load Registry Attributes
	results, err := Query(reg.tx,
		`SELECT Attributes FROM Registries WHERE SID=?`,
		reg.DbSID)
	defer results.Close()
	if err != nil {
		log.Printf("Error loading registries(%s): %s", reg.UID, err)
		return nil
	}
	row := results.NextRow()
	if row == nil {
		log.Printf("Can't find registry: %s", reg.UID)
		return nil
	}

	if row[0] != nil {
		Unmarshal([]byte(NotNilString(row[0])), &model.Attributes)
	}
	results.Close()

	model.Attributes.SetRegistry(reg)
	model.Attributes.SetSpecPropsFields()

	// Load Schemas
	results, err = Query(reg.tx, `
        SELECT RegistrySID, "Schema" FROM "Schemas"
        WHERE RegistrySID=?
        ORDER BY "Schema" ASC`, reg.DbSID)
	defer results.Close()

	if err != nil {
		log.Printf("Error loading schemas(%s): %s", reg.UID, err)
		return nil
	}

	for row := results.NextRow(); row != nil; row = results.NextRow() {
		model.Schemas = append(model.Schemas, NotNilString(row[1]))
	}
	results.Close()

	// Load Groups & Resources
	results, err = Query(reg.tx, `
        SELECT
            SID, RegistrySID, ParentSID, Plural, Singular, Attributes,
			Versions, VersionId, Latest, HasDocument
        FROM ModelEntities
        WHERE RegistrySID=?
        ORDER BY ParentSID ASC`, reg.DbSID)
	defer results.Close()

	if err != nil {
		log.Printf("Error loading model(%s): %s", reg.UID, err)
		return nil
	}

	for row := results.NextRow(); row != nil; row = results.NextRow() {
		attrs := (Attributes)(nil)
		if row[5] != nil {
			// json.Unmarshal([]byte(NotNilString(row[5])), &attrs)
			Unmarshal([]byte(NotNilString(row[5])), &attrs)
		}

		if *row[2] == nil { // ParentSID nil -> new Group
			g := &GroupModel{ // Plural
				SID:        NotNilString(row[0]), // SID
				Registry:   reg,
				Plural:     NotNilString(row[3]), // Plural
				Singular:   NotNilString(row[4]), // Singular
				Attributes: attrs,

				Resources: map[string]*ResourceModel{},
			}

			g.Attributes.SetSpecPropsFields()

			model.Groups[NotNilString(row[3])] = g
			groups[NotNilString(row[0])] = g

		} else { // New Resource
			g := groups[NotNilString(row[2])] // Parent SID

			if g != nil { // should always be true, but...
				r := &ResourceModel{
					SID:         NotNilString(row[0]),
					GroupModel:  g,
					Plural:      NotNilString(row[3]),
					Singular:    NotNilString(row[4]),
					Attributes:  attrs,
					Versions:    NotNilIntDef(row[6], VERSIONS),
					VersionId:   NotNilBoolDef(row[7], VERSIONID),
					Latest:      NotNilBoolDef(row[8], LATEST),
					HasDocument: NotNilBoolDef(row[9], HASDOCUMENT),
				}

				r.Attributes.SetSpecPropsFields()

				g.Resources[r.Plural] = r
			}
		}
	}
	results.Close()

	reg.Model = model
	return model
}

func (m *Model) FindGroupModel(gTypePlural string) *GroupModel {
	for _, gModel := range m.Groups {
		if strings.EqualFold(gModel.Plural, gTypePlural) {
			return gModel
		}
	}
	return nil
}

func (m *Model) ApplyNewModel(newM *Model) error {
	// Delete old Schemas, then add new ones
	m.Schemas = []string{XREGSCHEMA + "/" + SPECVERSION}
	err := Do(m.Registry.tx,
		`DELETE FROM "Schemas" WHERE RegistrySID=?`, m.Registry.DbSID)
	if err != nil {
		return err
	}

	for _, schema := range newM.Schemas {
		if err = m.AddSchema(schema); err != nil {
			return err
		}
	}

	m.Attributes = newM.Attributes

	// Find all old groups that need to be deleted
	for gmPlural, gm := range m.Groups {
		if newGM, ok := newM.Groups[gmPlural]; !ok {
			if err := gm.Delete(); err != nil {
				return err
			}
		} else {
			for rmPlural, rm := range gm.Resources {
				if _, ok := newGM.Resources[rmPlural]; !ok {
					if err := rm.Delete(); err != nil {
						return err
					}
				}
			}
		}
	}

	// Apply new stuff
	newM.Registry = m.Registry
	for _, newGM := range newM.Groups {
		newGM.Registry = m.Registry
		oldGM := m.Groups[newGM.Plural]
		if oldGM == nil {
			oldGM, err = m.AddGroupModel(newGM.Plural, newGM.Singular)
			if err != nil {
				return err
			}
		} else {
			oldGM.Singular = newGM.Singular
		}
		oldGM.Attributes = newGM.Attributes

		for _, newRM := range newGM.Resources {
			oldRM := oldGM.Resources[newRM.Plural]
			if oldRM == nil {
				oldRM, err = oldGM.AddResourceModel(newRM.Plural,
					newRM.Singular, newRM.Versions, newRM.VersionId,
					newRM.Latest, newRM.HasDocument)
			} else {
				oldRM.Singular = newRM.Singular
				oldRM.Versions = newRM.Versions
				oldRM.VersionId = newRM.VersionId
				oldRM.Latest = newRM.Latest
				oldRM.HasDocument = newRM.HasDocument
			}
			oldRM.Attributes = newRM.Attributes
		}
	}

	if err := m.Save(); err != nil {
		return err
	}

	return nil
}

func (gm *GroupModel) Delete() error {
	log.VPrintf(3, ">Enter: Delete.GroupModel: %s", gm.Plural)
	defer log.VPrintf(3, "<Exit: Delete.GroupModel")
	err := DoOne(gm.Registry.tx, `
        DELETE FROM ModelEntities
		WHERE RegistrySID=? AND SID=?`, // SID should be enough, but ok
		gm.Registry.DbSID, gm.SID)
	if err != nil {
		log.Printf("Error deleting groupModel(%s): %s", gm.Plural, err)
		return err
	}

	delete(gm.Registry.Model.Groups, gm.Plural)

	return nil
}

func (gm *GroupModel) Save() error {
	// Just updates this GroupModel, not any Resources
	// DO NOT use this to insert a new one

	buf, _ := json.Marshal(gm.Attributes)
	attrs := string(buf)

	err := DoZeroTwo(gm.Registry.tx, `
        INSERT INTO ModelEntities(
            SID, RegistrySID,
			ParentSID, Plural, Singular, Attributes)
        VALUES(?,?,?,?,?,?)
        ON DUPLICATE KEY UPDATE
		    ParentSID=?,Plural=?,Singular=?,Attributes=?
		`,
		gm.SID, gm.Registry.DbSID,
		nil, gm.Plural, gm.Singular, attrs,
		nil, gm.Plural, gm.Singular, attrs)
	if err != nil {
		log.Printf("Error updating groupModel(%s): %s", gm.Plural, err)
	}

	for _, rm := range gm.Resources {
		if err := rm.Save(); err != nil {
			return err
		}
	}

	return err
}

func (gm *GroupModel) AddAttr(name, daType string) (*Attribute, error) {
	return gm.AddAttribute(&Attribute{Name: name, Type: daType})
}

func (gm *GroupModel) AddAttrMap(name string, item *Item) (*Attribute, error) {
	return gm.AddAttribute(&Attribute{Name: name, Type: MAP, Item: item})
}

func (gm *GroupModel) AddAttrObj(name string) (*Attribute, error) {
	return gm.AddAttribute(&Attribute{Name: name, Type: OBJECT})
}

func (gm *GroupModel) AddAttrArray(name string, item *Item) (*Attribute, error) {
	return gm.AddAttribute(&Attribute{Name: name, Type: ARRAY, Item: item})
}

func (gm *GroupModel) AddAttribute(attr *Attribute) (*Attribute, error) {
	if attr == nil {
		return nil, nil
	}

	if attr.Name != "*" && !IsValidAttributeName(attr.Name) {
		return nil, fmt.Errorf("Invalid attribute name: %s", attr.Name)
	}

	if gm.Attributes == nil {
		gm.Attributes = Attributes{}
	}

	gm.Attributes[attr.Name] = attr

	if attr.Registry == nil {
		attr.Registry = gm.Registry
	}
	attr.Item.SetRegistry(gm.Registry)

	if err := gm.Registry.Model.Save(); err != nil {
		return nil, err
	}

	return attr, nil
}

func (gm *GroupModel) DelAttribute(name string) error {
	if gm.Attributes == nil {
		return nil
	}

	delete(gm.Attributes, name)

	return gm.Registry.Model.Save()
}

func (gm *GroupModel) AddResourceModel(plural string, singular string, versions int, verId bool, latest bool, hasDocument bool) (*ResourceModel, error) {
	if plural == "" {
		return nil, fmt.Errorf("Can't add a group with an empty plural name")
	}
	if singular == "" {
		return nil, fmt.Errorf("Can't add a group with an empty sigular name")
	}
	if versions < 0 {
		return nil, fmt.Errorf("'versions'(%d) must be >= 0", versions)
	}
	if !IsValidAttributeName(plural) {
		return nil, fmt.Errorf("ResourceModel plural name is not valid")
	}
	if !IsValidAttributeName(singular) {
		return nil, fmt.Errorf("ResourceModel singular name is not valid")
	}

	for _, r := range gm.Resources {
		if r.Plural == plural {
			return nil, fmt.Errorf("Resoucre model plural %q already "+
				"exists for group %q", plural, gm.Plural)
		}
		if r.Singular == singular {
			return nil,
				fmt.Errorf("Resoucre model singular %q already "+
					"exists for group %q", singular, gm.Plural)
		}
	}

	mSID := NewUUID()

	err := DoOne(gm.Registry.tx, `
		INSERT INTO ModelEntities(
			SID, RegistrySID, ParentSID, Plural, Singular, Versions,
			VersionId, Latest, HasDocument)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		mSID, gm.Registry.DbSID, gm.SID, plural, singular, versions,
		verId, latest, hasDocument)
	if err != nil {
		log.Printf("Error inserting resourceModel(%s): %s", plural, err)
		return nil, err
	}
	r := &ResourceModel{
		SID:         mSID,
		GroupModel:  gm,
		Singular:    singular,
		Plural:      plural,
		Versions:    versions,
		VersionId:   verId,
		Latest:      latest,
		HasDocument: hasDocument,
	}

	gm.Resources[plural] = r

	if err = gm.Registry.Model.Save(); err != nil {
		return nil, err
	}

	return r, nil
}

func (rm *ResourceModel) Delete() error {
	log.VPrintf(3, ">Enter: Delete.ResourceModel: %s", rm.Plural)
	defer log.VPrintf(3, "<Exit: Delete.ResourceModel")
	err := DoOne(rm.GroupModel.Registry.tx, `
        DELETE FROM ModelEntities
		WHERE RegistrySID=? AND SID=?`, // SID should be enough, but ok
		rm.GroupModel.Registry.DbSID, rm.SID)
	if err != nil {
		log.Printf("Error deleting resourceModel(%s): %s", rm.Plural, err)
		return err
	}

	delete(rm.GroupModel.Resources, rm.Plural)

	return nil
}

func (rm *ResourceModel) Save() error {
	// Just updates this GroupModel, not any Resources
	// DO NOT use this to insert a new one

	buf, _ := json.Marshal(rm.Attributes)
	attrs := string(buf)

	err := DoZeroTwo(rm.GroupModel.Registry.tx, `
        INSERT INTO ModelEntities(
            SID, RegistrySID,
			ParentSID, Plural, Singular, Versions,
			Attributes,
			VersionId, Latest, HasDocument)
        VALUES(?,?,?,?,?,?,?,?,?,?)
        ON DUPLICATE KEY UPDATE
            ParentSID=?, Plural=?, Singular=?,
			Attributes=?,
            Versions=?, VersionId=?, Latest=?, HasDocument=?`,
		rm.SID, rm.GroupModel.Registry.DbSID,
		rm.GroupModel.SID, rm.Plural, rm.Singular, rm.Versions,
		attrs,
		rm.VersionId, rm.Latest, rm.HasDocument,

		rm.GroupModel.SID, rm.Plural, rm.Singular,
		attrs,
		rm.Versions, rm.VersionId, rm.Latest, rm.HasDocument)
	if err != nil {
		log.Printf("Error updating resourceModel(%s): %s", rm.Plural, err)
		return err
	}
	return err
}

func (rm *ResourceModel) AddAttr(name, daType string) (*Attribute, error) {
	return rm.AddAttribute(&Attribute{Name: name, Type: daType})
}

func (rm *ResourceModel) AddAttrMap(name string, item *Item) (*Attribute, error) {
	return rm.AddAttribute(&Attribute{Name: name, Type: MAP, Item: item})
}

func (rm *ResourceModel) AddAttrObj(name string) (*Attribute, error) {
	return rm.AddAttribute(&Attribute{Name: name, Type: OBJECT})
}

func (rm *ResourceModel) AddAttrArray(name string, item *Item) (*Attribute, error) {
	return rm.AddAttribute(&Attribute{Name: name, Type: ARRAY, Item: item})
}

func (rm *ResourceModel) AddAttribute(attr *Attribute) (*Attribute, error) {
	if attr == nil {
		return nil, nil
	}

	if attr.Name != "*" && !IsValidAttributeName(attr.Name) {
		return nil, fmt.Errorf("Invalid attribute name: %s", attr.Name)
	}

	if rm.Attributes == nil {
		rm.Attributes = Attributes{}
	}

	rm.Attributes[attr.Name] = attr

	if attr.Registry == nil {
		attr.Registry = rm.GroupModel.Registry
	}
	attr.Item.SetRegistry(rm.GroupModel.Registry)

	if err := rm.GroupModel.Registry.Model.Save(); err != nil {
		return nil, err
	}

	return attr, nil
}

func (rm *ResourceModel) DelAttribute(name string) error {
	if rm.Attributes == nil {
		return nil
	}

	delete(rm.Attributes, name)

	return rm.GroupModel.Registry.Model.Save()
}

func (attrs *Attributes) SetRegistry(reg *Registry) {
	if attrs == nil {
		return
	}

	for _, attr := range *attrs {
		attr.Registry = reg
		attr.Item.SetRegistry(reg)
		attr.IfValues.SetRegistry(reg)
	}
}

func (attrs Attributes) AddIfValuesAttributes(obj map[string]any) {
	attrNames := Keys(attrs)
	for i := 0; i < len(attrNames); i++ { // since attrs changes
		attr := attrs[attrNames[i]]
		if len(attr.IfValues) == 0 || attr.Name == "*" {
			continue
		}

		val, ok := obj[attr.Name]
		if !ok {
			continue
		}

		valStr := fmt.Sprintf("%v", val)
		for ifValStr, ifValueData := range attr.IfValues {
			if ifValStr != valStr {
				continue
			}

			for _, newAttr := range ifValueData.SiblingAttributes {
				if _, ok := attrs[newAttr.Name]; ok {
					Panicf(`Attribute %q has an ifvalue(%s) that `+
						`defines a conflicting siblingattribute: %s`,
						attr.Name, ifValStr, newAttr.Name)
				}
				attrs[newAttr.Name] = newAttr
				attrNames = append(attrNames, newAttr.Name)
			}
		}
	}
}

func KindIsScalar(k reflect.Kind) bool {
	// SOOOO risky :-)
	return k < reflect.Array || k == reflect.String
}

func IsScalar(daType string) bool {
	return daType == BOOLEAN || daType == DECIMAL || daType == INTEGER ||
		daType == STRING || daType == TIMESTAMP || daType == UINTEGER ||
		daType == URI || daType == URI_REFERENCE || daType == URI_TEMPLATE ||
		daType == URL
}

// Is some string variant
func IsString(daType string) bool {
	return daType == STRING || daType == TIMESTAMP ||
		daType == URI || daType == URI_REFERENCE || daType == URI_TEMPLATE ||
		daType == URL
}

func (a *Attribute) InLevel(level int) bool {
	return a.internals.levels == "" ||
		strings.ContainsRune(a.internals.levels, rune('0'+level))
}

func (a *Attribute) IsScalar() bool {
	return IsScalar(a.Type)
}

func (a *Attribute) SetRegistry(reg *Registry) {
	if a == nil {
		return
	}

	a.Registry = reg
	a.Item.SetRegistry(reg)
	a.IfValues.SetRegistry(reg)
}

func (a *Attribute) AddAttr(name, daType string) (*Attribute, error) {
	return a.AddAttribute(&Attribute{
		Registry: a.Registry,
		Name:     name,
		Type:     daType,
	})
}

func (a *Attribute) AddAttrMap(name string, item *Item) (*Attribute, error) {
	return a.AddAttribute(&Attribute{Name: name, Type: MAP, Item: item})
}

func (a *Attribute) AddAttrObj(name string) (*Attribute, error) {
	return a.AddAttribute(&Attribute{Name: name, Type: OBJECT})
}

func (a *Attribute) AddAttrArray(name string, item *Item) (*Attribute, error) {
	return a.AddAttribute(&Attribute{Name: name, Type: ARRAY, Item: item})
}
func (a *Attribute) AddAttribute(attr *Attribute) (*Attribute, error) {
	if attr.Name != "*" && !IsValidAttributeName(attr.Name) {
		return nil, fmt.Errorf("Invalid attribute name: %s", attr.Name)
	}

	if a.Attributes == nil {
		a.Attributes = Attributes{}
	}

	a.Attributes[attr.Name] = attr
	attr.SetRegistry(a.Registry)

	if err := a.Registry.Model.Save(); err != nil {
		return nil, err
	}
	return attr, nil
}

// Make sure that the attribute doesn't deviate too much from the
// spec defined version of it. There's only so much that we allow the
// user to customize
func EnsureAttrOK(userAttr *Attribute, specAttr *Attribute) error {
	// Just blindly ignore any updates made to "model"
	if userAttr.Name == "model" {
		*userAttr = *specAttr
		return nil
	}

	if specAttr.ServerRequired {
		if userAttr.ServerRequired == false {
			return fmt.Errorf(`"model.%s" must have its "serverrequired" `+
				`attribute set to "true"`, userAttr.Name)
		}
		if specAttr.ReadOnly && !userAttr.ReadOnly {
			return fmt.Errorf(`"model.%s" must have its "readonly" `+
				`attribute set to "true"`, userAttr.Name)
		}
	}

	if specAttr.Type != userAttr.Type {
		return fmt.Errorf(`"model.%s" must have a "type" of %q`,
			userAttr.Name, specAttr.Type)
	}

	return nil
}

func (m *Model) Verify() error {
	found := false
	for _, s := range m.Schemas {
		if strings.EqualFold(s, XREGSCHEMA+"/"+SPECVERSION) {
			found = true
			break
		}
	}
	if !found {
		m.Schemas = append([]string{XREGSCHEMA + "/" + SPECVERSION}, m.Schemas...)
	}
	sort.Strings(m.Schemas)

	// First, make sure we have the xRegistry core/spec defined attributes
	// in the list and they're not changed in an inappropriate way.
	// This just checks the Registry.Attributes. Groups and Resources will
	// be done in their own Verify funcs

	if m.Attributes == nil {
		m.Attributes = Attributes{}
	}

	for _, specProp := range OrderedSpecProps {
		// If it's not a Registry level attribute, then skip it
		if !specProp.InLevel(0) {
			continue
		}

		modelAttr, ok := m.Attributes[specProp.Name]
		if !ok {
			// Missing in model, so add it
			m.Attributes[specProp.Name] = specProp
			continue
		}

		// It's there but make sure it's not changed in a bad way
		if err := EnsureAttrOK(modelAttr, specProp); err != nil {
			return err
		}
	}

	// Now check Registry attributes for correctness
	ld := &LevelData{
		AttrNames: map[string]bool{},
		Path:      NewPPP("model"),
	}
	if err := m.Attributes.Verify(ld); err != nil {
		return err
	}

	for gmName, gm := range m.Groups {
		if err := gm.Verify(gmName); err != nil {
			return err
		}
	}

	return nil
}

func (gm *GroupModel) Verify(gmName string) error {
	if !IsValidAttributeName(gmName) {
		return fmt.Errorf("Invalid Group name/key %q - must match %q",
			gmName, RegexpPropName.String())
	}

	if gm.Plural != gmName {
		return fmt.Errorf("Group %q must have a `plural` value of %q, not %q",
			gmName, gmName, gm.Plural)
	}

	if !IsValidAttributeName(gm.Singular) {
		return fmt.Errorf("Invalid Group 'singular' value %q - must match %q",
			gm.Singular, RegexpPropName.String())
	}

	// Make sure we have the xRegistry core/spec defined attributes
	// in the list and they're not changed in an inappropriate way.
	// This just checks the Group level Attributes
	if gm.Attributes == nil {
		gm.Attributes = Attributes{}
	}

	for _, specProp := range OrderedSpecProps {
		// If it's not a Group level attribute, then skip it
		if !specProp.InLevel(1) {
			continue
		}

		modelAttr, ok := gm.Attributes[specProp.Name]
		if !ok {
			// Missing in model, so add it
			gm.Attributes[specProp.Name] = specProp
			continue
		}

		// It's there but make sure it's not changed in a bad way
		if err := EnsureAttrOK(modelAttr, specProp); err != nil {
			return err
		}
	}

	ld := &LevelData{
		AttrNames: map[string]bool{},
		Path:      NewPPP("groups").P(gm.Plural),
	}
	if err := gm.Attributes.Verify(ld); err != nil {
		return err
	}

	for rmName, rm := range gm.Resources {
		if err := rm.Verify(rmName); err != nil {
			return err
		}
	}

	return nil
}

func (gm *GroupModel) SetRegistry(reg *Registry) {
	if gm == nil {
		return
	}

	gm.Registry = reg
	gm.Attributes.SetRegistry(reg)

	for _, rm := range gm.Resources {
		rm.GroupModel = gm
		rm.SetRegistry(reg)
	}
}

func (rm *ResourceModel) Verify(rmName string) error {
	if !IsValidAttributeName(rmName) {
		return fmt.Errorf("Invalid Resource name/key %q - must match %q",
			rmName, RegexpPropName.String())
	}

	if rm.Plural == "" {
		return fmt.Errorf("Resource %q is missing a \"name\" value", rmName)
	}
	if rm.Plural != rmName {
		return fmt.Errorf("Resource %q must have a 'plural' value of %q, "+
			"not %q", rmName, rmName, rm.Plural)
	}

	if rm.Versions < 0 {
		return fmt.Errorf("Resource %q must have a 'versions' value >= 0",
			rmName)
	}

	// Make sure we have the xRegistry core/spec defined attributes
	// in the list and they're not changed in an inappropriate way.
	// This just checks the Group level Attributes
	if rm.Attributes == nil {
		rm.Attributes = Attributes{}
	}

	for _, specProp := range OrderedSpecProps {
		// If it's not a Resource level attribute, then skip it
		if !specProp.InLevel(3) && !specProp.InLevel(2) {
			continue
		}

		modelAttr, ok := rm.Attributes[specProp.Name]
		if !ok {
			// Missing in model, so add it
			rm.Attributes[specProp.Name] = specProp
			continue
		}

		// It's there but make sure it's not changed in a bad way
		if err := EnsureAttrOK(modelAttr, specProp); err != nil {
			return err
		}
	}

	ld := &LevelData{
		AttrNames: map[string]bool{},
		Path:      NewPPP("resources").P(rm.Plural),
	}
	if err := rm.Attributes.Verify(ld); err != nil {
		return err
	}

	return nil
}

func (rm *ResourceModel) SetRegistry(reg *Registry) {
	if rm == nil {
		return
	}

	rm.Attributes.SetRegistry(reg)
}

type LevelData struct {
	// AttrNames is the list of known attribute names for a certain level
	// an entity (basically the Attributes list + ifValues). We use this to know
	// if an IfValue SiblingAttribute would conflict if another attribute's name
	AttrNames map[string]bool
	Path      *PropPath
}

func (attrs Attributes) Verify(ld *LevelData) error {
	ld = &LevelData{
		AttrNames: maps.Clone(ld.AttrNames),
		Path:      ld.Path.Clone(),
	}
	if ld.AttrNames == nil {
		ld.AttrNames = map[string]bool{}
	}

	// First add the new attribute names, while checking the attr
	for name, attr := range attrs {
		if name == "" { // attribute key empty?
			return fmt.Errorf("%q has an empty attribute key", ld.Path.UI())
		}
		if ld.AttrNames[name] == true { // Dup attr name?
			return fmt.Errorf("Duplicate attribute name (%s) at: %s", name,
				ld.Path.UI())
		}
		if name != "*" && !IsValidAttributeName(name) { // valid chars?
			return fmt.Errorf("%q has an invalid attribute key %q - must "+
				"match %q", ld.Path.UI(), name, RegexpPropName.String())
		}
		path := ld.Path.P(name)
		if name != attr.Name { // missing Nmae: field?
			return fmt.Errorf("%q must have a \"name\" set to %q", path.UI(),
				name)
		}
		if attr.Type == "" {
			return fmt.Errorf("%q is missing a \"type\"", path.UI())
		}
		if DefinedTypes[attr.Type] != true { // valie Type: field?
			return fmt.Errorf("%q has an invalid type: %s", path.UI(),
				attr.Type)
		}

		// Is it ok for strict=true and enum=[] ? Require no value???
		// if attr.Strict == true && len(attr.Enum) == 0 {
		// }

		// check enum values
		if attr.Enum != nil && len(attr.Enum) == 0 {
			return fmt.Errorf("%q specifies an \"enum\" but it is empty",
				path.UI())
		}
		if len(attr.Enum) > 0 {
			if IsScalar(attr.Type) != true {
				return fmt.Errorf("%q is not a scalar, so \"enum\" is not "+
					"allowed", path.UI())
			}

			for _, val := range attr.Enum {
				if !IsOfType(val, attr.Type) {
					return fmt.Errorf("%q enum value \"%v\" must be of type %q",
						path.UI(), val, attr.Type)
				}
			}
		}

		if attr.ClientRequired && !attr.ServerRequired {
			return fmt.Errorf("%q must have \"serverrequired\" "+
				"since \"clientrequired\" is \"true\"",
				path.UI())
		}

		if !IsNil(attr.Default) {
			if !attr.ServerRequired {
				return fmt.Errorf("%q must have \"serverrequired\" "+
					"since a \"default\" value is provided",
					path.UI())
			}

			if IsScalar(attr.Type) != true {
				return fmt.Errorf("%q is not a scalar, so \"default\" is not "+
					"allowed", path.UI())
			}

			val := attr.Default
			if !IsOfType(val, attr.Type) {
				return fmt.Errorf("%q \"default\" value must be of type %q",
					path.UI(), attr.Type)
			}
		}

		// Object doesn't need an Item, but maps and arrays do
		if attr.Type == MAP || attr.Type == ARRAY {
			if attr.Item == nil {
				return fmt.Errorf("%q must have an \"item\" section", path.UI())
			}
		}

		if attr.Type == OBJECT {
			if attr.Item != nil {
				return fmt.Errorf("%q must not have an \"item\" section", path.UI())
			}
			if err := attr.Attributes.Verify(&LevelData{nil, path}); err != nil {
				return err
			}
		}

		if attr.Item != nil {
			if err := attr.Item.Verify(path); err != nil {
				return err
			}
		}

		ld.AttrNames[attr.Name] = true
	}

	// Now that we have all of the attribute names for this level, go ahead
	// and check the IfValues, not just for validatity but to also make sure
	// they don't define duplicate attribute names
	for _, attr := range attrs {
		for valStr, ifValue := range attr.IfValues {
			if valStr == "" {
				return fmt.Errorf("%q has an empty ifvalues key", ld.Path.UI())
			}
			nextLD := &LevelData{ld.AttrNames,
				ld.Path.P(attr.Name).P("ifvalues").P(valStr)}
			if err := ifValue.SiblingAttributes.Verify(nextLD); err != nil {
				return err
			}
		}
	}

	return nil
}

// Copy the internal data for spec defined properties so we can access
// that info directly from these Attributes instead of having to go back
// to the SpecProps stuff
func (attrs Attributes) SetSpecPropsFields() {
	for k, attr := range attrs {
		if specProp := SpecProps[k]; specProp != nil {
			attr.internals = specProp.internals
		}
	}
}

func (ifvalues IfValues) SetRegistry(reg *Registry) {
	if ifvalues == nil {
		return
	}

	for _, ifvalue := range ifvalues {
		ifvalue.SiblingAttributes.SetRegistry(reg)
	}
}

func (item *Item) Verify(path *PropPath) error {
	p := path.P("item")

	if item.Type == "" {
		return fmt.Errorf("%q must have a \"type\" defined", p.UI())
	}

	if DefinedTypes[item.Type] != true {
		return fmt.Errorf("%q has an invalid \"type\": %s", p.UI(),
			item.Type)
	}

	if item.Type != OBJECT && item.Attributes != nil {
		return fmt.Errorf("%q must not have \"attributes\"", p.UI())
	}

	if item.Type == MAP || item.Type == ARRAY {
		if item.Item == nil {
			return fmt.Errorf("%q must have an \"item\" section", p.UI())
		}
	}

	if item.Attributes != nil {
		if err := item.Attributes.Verify(&LevelData{nil, p}); err != nil {
			return err
		}
	}

	if item.Item != nil {
		return item.Item.Verify(p)
	}
	return nil
}

var DefinedTypes = map[string]bool{
	ANY:     true,
	BOOLEAN: true,
	DECIMAL: true, INTEGER: true, UINTEGER: true,
	ARRAY:     true,
	MAP:       true,
	OBJECT:    true,
	STRING:    true,
	TIMESTAMP: true,
	URI:       true, URI_REFERENCE: true, URI_TEMPLATE: true, URL: true}

// attr.Type must be a scalar
// Used to check JSON type vs our types
func IsOfType(val any, attrType string) bool {
	switch reflect.ValueOf(val).Kind() {
	case reflect.Bool:
		return attrType == BOOLEAN

	case reflect.String:
		if attrType == TIMESTAMP {
			str := val.(string)
			_, err := time.Parse(time.RFC3339, str)
			return err == nil
		}

		return IsString(attrType)

	case reflect.Float64: // JSON ints show up as floats
		if attrType == DECIMAL {
			return true
		}
		if attrType == INTEGER || attrType == UINTEGER {
			valInt := int(val.(float64))
			if float64(valInt) != val.(float64) {
				return false
			}
			return attrType == INTEGER || valInt >= 0
		}
		return false

	case reflect.Int:
		if attrType == DECIMAL {
			return true
		}
		if attrType == INTEGER || attrType == UINTEGER {
			valInt := val.(int)
			return attrType == INTEGER || valInt >= 0
		}
		return false

	default:
		return false
	}
}

/*
func AbstractToSingular(reg *Registry, abs string) string {
	absParts := strings.Split(abs, string(DB_IN))

	if len(absParts) == 0 {
		panic("help")
	}
	gm := reg.Model.Groups[absParts[0]]
	PanicIf(gm == nil, "no gm")

	if len(absParts) == 1 {
		return gm.Singular
	}

	rm := gm.Resources[absParts[1]]
	PanicIf(rm == nil, "no rm")
	return rm.Singular
}
*/

// The model serializer we use for the "xRegistry" schema format
func Model2xRegistryJson(m *Model, format string) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

func GetModelSerializer(format string) ModelSerializer {
	format = strings.ToLower(format)
	searchParts := strings.SplitN(format, "/", 2)
	if searchParts[0] == "" {
		return nil
	}
	if len(searchParts) == 1 {
		searchParts = append(searchParts, "")
	}

	result := ModelSerializer(nil)
	resultVersion := ""

	for format, sm := range ModelSerializers {
		format = strings.ToLower(format)
		parts := strings.SplitN(format, "/", 2)
		if searchParts[0] != parts[0] {
			continue
		}
		if len(parts) == 1 {
			parts = append(parts, "")
		}

		if searchParts[1] != "" {
			if searchParts[1] == parts[1] {
				// Exact match - stop immediately
				result = sm
				break
			}
			// Looking for an exact match - not it so skip it
			continue
		}

		if resultVersion == "" || strings.Compare(parts[1], resultVersion) > 0 {
			result = sm
			resultVersion = parts[1]
		}
	}

	return result
}

func RegisterModelSerializer(name string, sm ModelSerializer) {
	ModelSerializers[name] = sm
}

func init() {
	RegisterModelSerializer(XREGSCHEMA+"/"+SPECVERSION, Model2xRegistryJson)
}
