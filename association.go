package rel

import (
	"reflect"
	"strings"
	"sync"

	"github.com/serenize/snaker"
)

// AssociationType defines the type of association in database.
type AssociationType uint8

const (
	// BelongsTo association.
	BelongsTo = iota
	// HasOne association.
	HasOne
	// HasMany association.
	HasMany
	// ManyToMany association.
	ManyToMany
)

type associationKey struct {
	rt    reflect.Type
	index int
}

type associationData struct {
	typ              AssociationType
	targetIndex      []int
	referenceField   string
	referenceIndex   int
	referenceThrough string
	foreignField     string
	foreignIndex     int
	foreignThrough   string
	through          string
	autosave         bool
}

var associationCache sync.Map

// Association provides abstraction to work with association of document or collection.
type Association struct {
	data associationData
	rv   reflect.Value
}

// Type of association.
func (a Association) Type() AssociationType {
	return a.data.typ
}

// Document returns association target as document.
// If association is zero, second return value will be false.
func (a Association) Document() (*Document, bool) {
	var (
		rv = a.rv.FieldByIndex(a.data.targetIndex)
	)

	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
			return NewDocument(rv), false
		}

		var (
			doc = NewDocument(rv)
		)

		return doc, doc.Persisted()
	default:
		var (
			doc = NewDocument(rv.Addr())
		)

		return doc, doc.Persisted()
	}
}

// Collection returns association target as collection.
// If association is zero, second return value will be false.
func (a Association) Collection() (*Collection, bool) {
	var (
		rv     = a.rv.FieldByIndex(a.data.targetIndex)
		loaded = !rv.IsNil()
	)

	if rv.Kind() == reflect.Ptr {
		if !loaded {
			rv.Set(reflect.New(rv.Type().Elem()))
			rv.Elem().Set(reflect.MakeSlice(rv.Elem().Type(), 0, 0))
		}

		return NewCollection(rv), loaded
	}

	if !loaded {
		rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
	}

	return NewCollection(rv.Addr()), loaded
}

// IsZero returns true if association is not loaded.
func (a Association) IsZero() bool {
	var (
		rv = a.rv.FieldByIndex(a.data.targetIndex)
	)

	return isDeepZero(reflect.Indirect(rv), 1)
}

// ReferenceField of the association.
func (a Association) ReferenceField() string {
	return a.data.referenceField
}

// ReferenceThrough return intermediary foreign field used for many to many association.
func (a Association) ReferenceThrough() string {
	return a.data.referenceThrough
}

// ReferenceValue of the association.
func (a Association) ReferenceValue() interface{} {
	return indirect(a.rv.Field(a.data.referenceIndex))
}

// ForeignField of the association.
func (a Association) ForeignField() string {
	return a.data.foreignField
}

// ForeignThrough return intermediary foreign field used for many to many association.
func (a Association) ForeignThrough() string {
	return a.data.foreignThrough
}

// ForeignValue of the association.
// It'll panic if association type is has many.
func (a Association) ForeignValue() interface{} {
	if a.Type() == HasMany || a.Type() == ManyToMany {
		panic("cannot infer foreign value for has many or many to many association")
	}

	var (
		rv = a.rv.FieldByIndex(a.data.targetIndex)
	)

	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	return indirect(rv.Field(a.data.foreignIndex))
}

// Through return intermediary table used for many to many association.
func (a Association) Through() string {
	return a.data.through
}

// Autosave setting when parent is created/updated/deleted.
func (a Association) Autosave() bool {
	return a.data.autosave
}

func newAssociation(rv reflect.Value, index int) Association {
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	return Association{
		data: extractAssociationData(rv.Type(), index),
		rv:   rv,
	}
}

func extractAssociationData(rt reflect.Type, index int) associationData {
	var (
		key = associationKey{
			rt:    rt,
			index: index,
		}
	)

	if val, cached := associationCache.Load(key); cached {
		return val.(associationData)
	}

	var (
		sf              = rt.Field(index)
		ft              = sf.Type
		ref, refThrough = getAssocField(sf.Tag, "ref")
		fk, fkThrough   = getAssocField(sf.Tag, "fk")
		through         = sf.Tag.Get("through")
		fName           = fieldName(sf)
		assocData       = associationData{
			targetIndex: sf.Index,
			autosave:    sf.Tag.Get("autosave") == "true",
		}
	)

	for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
		ft = ft.Elem()
	}

	var (
		refDocData = extractDocumentData(rt, true)
		fkDocData  = extractDocumentData(ft, true)
	)

	// Try to guess ref and fk if not defined.
	if ref == "" || fk == "" {
		// TODO: replace "id" with inferred primary field
		if through != "" {
			ref = "id"
			fk = "id"
			refThrough = snaker.CamelToSnake(rt.Name()) + "_id"
			fkThrough = snaker.CamelToSnake(ft.Name()) + "_id"
		} else if _, isBelongsTo := refDocData.index[fName+"_id"]; isBelongsTo {
			ref = fName + "_id"
			fk = "id"
		} else {
			ref = "id"
			fk = snaker.CamelToSnake(rt.Name()) + "_id"
		}
	}

	if id, exist := refDocData.index[ref]; !exist {
		panic("rel: references (" + ref + ") field not found ")
	} else {
		assocData.referenceIndex = id
		assocData.referenceField = ref
	}

	if id, exist := fkDocData.index[fk]; !exist {
		panic("rel: foreign_key (" + fk + ") field not found")
	} else {
		assocData.foreignIndex = id
		assocData.foreignField = fk
	}

	// guess assoc type
	if sf.Type.Kind() == reflect.Slice || (sf.Type.Kind() == reflect.Ptr && sf.Type.Elem().Kind() == reflect.Slice) {
		if through != "" {
			assocData.typ = ManyToMany
			assocData.referenceThrough = refThrough
			assocData.foreignThrough = fkThrough
			assocData.through = through
		} else {
			assocData.typ = HasMany
		}
	} else {
		if len(assocData.referenceField) > len(assocData.foreignField) {
			assocData.typ = BelongsTo
		} else {
			assocData.typ = HasOne
		}
	}

	associationCache.Store(key, assocData)

	return assocData
}

func getAssocField(tag reflect.StructTag, field string) (string, string) {
	fields := strings.Split(tag.Get(field), ":")
	if len(fields) == 2 {
		return fields[0], fields[1]
	}

	return fields[0], ""
}
