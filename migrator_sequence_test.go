package postgres

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type sequenceEvent struct {
	EventId string `gorm:"primaryKey"`
	Seq     int64  `gorm:"type:bigserial;autoIncrement"`
}

func (sequenceEvent) TableName() string { return "SequenceEvents" }

type addedSequenceEvent sequenceEvent

func (addedSequenceEvent) TableName() string { return "AddedSequenceEvents" }

type smallSequenceEvent struct {
	EventId string `gorm:"primaryKey"`
	Seq     int32  `gorm:"type:serial;autoIncrement"`
}

func (smallSequenceEvent) TableName() string { return "ResizedSequenceEvents" }

type resizedSequenceEvent sequenceEvent

func (resizedSequenceEvent) TableName() string { return "ResizedSequenceEvents" }

type removedSequenceEvent sequenceEvent

func (removedSequenceEvent) TableName() string { return "RemovedSequenceEvents" }

func openSequenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GORM_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set GORM_POSTGRES_DSN to run PostgreSQL migration tests")
	}
	db, err := gorm.Open(Open(dsn), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{NoLowerCase: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db
}

func TestAutoMigrate_MixedCaseSerialIsReadOnlySafe(t *testing.T) {
	db := openSequenceTestDB(t)
	if err := db.Migrator().DropTable(&sequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&sequenceEvent{}) })
	if err := db.AutoMigrate(&sequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	first := sequenceEvent{EventId: "first"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first.Seq == 0 {
		t.Fatal("insert did not generate a sequence value")
	}
	var ddl []string
	if err := db.Callback().Raw().Before("gorm:raw").Register("test:record_ddl", func(tx *gorm.DB) {
		statement := strings.ToUpper(strings.TrimSpace(tx.Statement.SQL.String()))
		for _, prefix := range []string{"CREATE ", "ALTER ", "DROP ", "COMMENT "} {
			if strings.HasPrefix(statement, prefix) {
				ddl = append(ddl, tx.Statement.SQL.String())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	// A schema check must recognize the existing serial column without DDL.
	tx := db.Begin(&sql.TxOptions{ReadOnly: true})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := tx.AutoMigrate(&sequenceEvent{}); err != nil {
		t.Fatalf("repeat migration in read-only transaction: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&sequenceEvent{}); err != nil {
		t.Fatalf("repeat writable migration: %v", err)
	}
	if len(ddl) != 0 {
		t.Fatalf("repeat migrations emitted DDL: %v", ddl)
	}
	second := sequenceEvent{EventId: "second"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("sequence did not advance: %d -> %d", first.Seq, second.Seq)
	}
}

func TestAutoMigrate_WidenMixedCaseSerial(t *testing.T) {
	db := openSequenceTestDB(t)
	if err := db.Migrator().DropTable(&smallSequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&smallSequenceEvent{}) })
	if err := db.AutoMigrate(&smallSequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&resizedSequenceEvent{}); err != nil {
		t.Fatalf("widen serial column and sequence: %v", err)
	}
	if err := db.Exec(`SELECT setval(pg_get_serial_sequence('"ResizedSequenceEvents"', 'Seq'), 2147483648, false)`).Error; err != nil {
		t.Fatal(err)
	}
	event := resizedSequenceEvent{EventId: "first"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Seq != 2147483648 {
		t.Fatalf("widened sequence generated %d", event.Seq)
	}
}

func TestDeleteSequence_MixedCaseColumn(t *testing.T) {
	db := openSequenceTestDB(t)
	if err := db.Migrator().DropTable(&removedSequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&removedSequenceEvent{}) })
	if err := db.AutoMigrate(&removedSequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	m := db.Migrator().(Migrator)
	if err := m.RunWithValue(&removedSequenceEvent{}, func(stmt *gorm.Statement) error {
		return m.DeleteSequence(db, stmt, stmt.Schema.LookUpField("Seq"), clause.Expr{SQL: "bigint"})
	}); err != nil {
		t.Fatalf("remove serial default: %v", err)
	}
	var hasDefault bool
	if err := db.Raw(`SELECT column_default IS NOT NULL FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'RemovedSequenceEvents' AND column_name = 'Seq'`).Scan(&hasDefault).Error; err != nil {
		t.Fatal(err)
	}
	if hasDefault {
		t.Fatal("serial default was not removed")
	}
	var hasSequence bool
	if err := db.Raw(`SELECT to_regclass('"RemovedSequenceEvents_Seq_seq"') IS NOT NULL`).Scan(&hasSequence).Error; err != nil {
		t.Fatal(err)
	}
	if hasSequence {
		t.Fatal("owned sequence was not removed")
	}
}

func TestAutoMigrate_AddMixedCaseSerial(t *testing.T) {
	db := openSequenceTestDB(t)
	if err := db.Migrator().DropTable(&addedSequenceEvent{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&addedSequenceEvent{}) })
	if err := db.Exec(`CREATE TABLE "AddedSequenceEvents" ("EventId" text PRIMARY KEY, "Seq" bigint)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&addedSequenceEvent{}); err != nil {
		t.Fatalf("add serial default to existing column: %v", err)
	}
	event := addedSequenceEvent{EventId: "first"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Seq == 0 {
		t.Fatal("insert did not generate a sequence value")
	}
	var sequence string
	if err := db.Raw(`SELECT pg_get_serial_sequence('"AddedSequenceEvents"', 'Seq')`).Scan(&sequence).Error; err != nil {
		t.Fatal(err)
	}
	if sequence != `public."AddedSequenceEvents_Seq_seq"` {
		t.Fatalf("sequence is not owned by the mixed-case column: %q", sequence)
	}
}
