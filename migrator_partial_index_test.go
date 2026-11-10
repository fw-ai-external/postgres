package postgres

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

type partialUniqueIndexModel struct {
	ID        uint   `gorm:"primaryKey"`
	SsoDomain string `gorm:"uniqueIndex:account_sso_domain_unique,where:\"SsoDomain\" <> ''"`
}

type plainUniqueIndexModel struct {
	ID        uint   `gorm:"primaryKey"`
	SsoDomain string `gorm:"uniqueIndex:account_sso_domain"`
}

func parseSchema(t *testing.T, model interface{}) *schema.Schema {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{NoLowerCase: true})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	_ = s.ParseIndexes()
	return s
}

func TestFieldHasPartialUniqueIndex_TrueForWhereTag(t *testing.T) {
	s := parseSchema(t, &partialUniqueIndexModel{})
	f := s.FieldsByDBName["SsoDomain"]
	// Exercise the MigrateColumn guard without depending on version-specific
	// ParseIndexes side effects on field.Unique.
	f.Unique = true
	if !fieldHasPartialUniqueIndex(s, f) {
		t.Fatal("expected fieldHasPartialUniqueIndex=true for uniqueIndex with where:")
	}
}

func TestFieldHasPartialUniqueIndex_FalseForPlainUniqueIndex(t *testing.T) {
	s := parseSchema(t, &plainUniqueIndexModel{})
	f := s.FieldsByDBName["SsoDomain"]
	f.Unique = true
	if fieldHasPartialUniqueIndex(s, f) {
		t.Fatal("expected fieldHasPartialUniqueIndex=false for uniqueIndex without where:")
	}
}
