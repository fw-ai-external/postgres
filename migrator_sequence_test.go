package postgres

import (
	"database/sql"
	"os"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type sequenceEvent struct {
	EventId string `gorm:"primaryKey"`
	Seq     int64  `gorm:"type:bigserial;autoIncrement"`
}

func (sequenceEvent) TableName() string { return "SequenceEvents" }

type addedSequenceEvent sequenceEvent

func (addedSequenceEvent) TableName() string { return "AddedSequenceEvents" }

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
	second := sequenceEvent{EventId: "second"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("sequence did not advance: %d -> %d", first.Seq, second.Seq)
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
