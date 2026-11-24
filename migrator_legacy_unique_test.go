package postgres

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

// LegacyOwnerFields holds the unique column in an embedded struct, so the
// tests also cover a field gorm flattens into the parent schema.
type LegacyOwnerFields struct {
	OwnerId *string `gorm:"uniqueIndex:idx_legacy_owner"`
}

type customUniqueIndexModel struct {
	ID uint `gorm:"primaryKey"`
	LegacyOwnerFields
}

func (customUniqueIndexModel) TableName() string { return "LegacyCustomUniques" }

type defaultUniqueIndexModel struct {
	ID           uint   `gorm:"primaryKey"`
	ExternalName string `gorm:"uniqueIndex"`
}

func (defaultUniqueIndexModel) TableName() string { return "LegacyAddedUniques" }

type constraintOnlyUniqueIndexModel struct {
	ID   uint   `gorm:"primaryKey"`
	Code string `gorm:"uniqueIndex"`
}

func (constraintOnlyUniqueIndexModel) TableName() string { return "LegacyConstraintOnlyUniques" }

type compositeUniqueIndexModel struct {
	ID        uint   `gorm:"primaryKey"`
	AccountId string `gorm:"uniqueIndex:idx_legacy_account_name"`
	Name      string `gorm:"uniqueIndex:idx_legacy_account_name"`
}

func (compositeUniqueIndexModel) TableName() string { return "LegacyCompositeUniques" }

type partialLegacyUniqueModel struct {
	ID        uint   `gorm:"primaryKey"`
	SsoDomain string `gorm:"uniqueIndex:idx_legacy_sso_domain,where:\"SsoDomain\" <> ''"`
}

func (partialLegacyUniqueModel) TableName() string { return "LegacyPartialUniques" }

type expressionUniqueIndexModel struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex:idx_legacy_email_lower,expression:lower(\"Email\")"`
}

type plainIndexModel struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"index"`
}

func TestFieldHasSingleColumnUniqueIndex(t *testing.T) {
	cases := []struct {
		name  string
		model interface{}
		field string
		want  bool
	}{
		{"custom-named uniqueIndex in an embedded struct", &customUniqueIndexModel{}, "OwnerId", true},
		{"default-named uniqueIndex", &defaultUniqueIndexModel{}, "ExternalName", true},
		{"composite uniqueIndex", &compositeUniqueIndexModel{}, "AccountId", false},
		{"partial uniqueIndex", &partialLegacyUniqueModel{}, "SsoDomain", false},
		{"expression uniqueIndex", &expressionUniqueIndexModel{}, "Email", false},
		{"plain index", &plainIndexModel{}, "Name", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parseSchema(t, tc.model)
			if got := fieldHasSingleColumnUniqueIndex(s, s.FieldsByDBName[tc.field]); got != tc.want {
				t.Fatalf("fieldHasSingleColumnUniqueIndex(%s) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

// Tables migrated by gorm <= v1.25.6 carry UNIQUE constraints on columns that
// are the only column of a unique index, named other than the
// "uni_<Table>_<Column>" later gorm versions look for. Planning and
// re-migrating them must issue no DDL and keep the constraints, instead of
// dropping "uni_<Table>_<Column>", which does not exist. Before gorm v1.25.7
// the override is never called, so only the later versions in the CI matrix
// exercise it.
func TestAutoMigrate_KeepsLegacyUniqueConstraintsOnUniqueIndexColumns(t *testing.T) {
	db := openSequenceTestDB(t)
	execAll(t, db,
		`DROP TABLE IF EXISTS "LegacyCustomUniques", "LegacyAddedUniques", "LegacyConstraintOnlyUniques"`,
		// A table created with a custom-named unique index: the next migration
		// added "idx_<Table>_<Column>" through AlterColumn.
		`CREATE TABLE "LegacyCustomUniques" ("ID" bigserial PRIMARY KEY, "OwnerId" text)`,
		`CREATE UNIQUE INDEX "idx_legacy_owner" ON "LegacyCustomUniques" ("OwnerId")`,
		`ALTER TABLE "LegacyCustomUniques" ADD CONSTRAINT "idx_LegacyCustomUniques_OwnerId" UNIQUE ("OwnerId")`,
		// A column added to an existing table as "ADD ... UNIQUE": PostgreSQL
		// named the constraint "<Table>_<Column>_key".
		`CREATE TABLE "LegacyAddedUniques" ("ID" bigserial PRIMARY KEY)`,
		`ALTER TABLE "LegacyAddedUniques" ADD "ExternalName" text UNIQUE`,
		`CREATE UNIQUE INDEX "idx_LegacyAddedUniques_ExternalName" ON "LegacyAddedUniques" ("ExternalName")`,
		// A default-named unique index added to an existing column: AlterColumn
		// runs before indexes are created, so it added "idx_<Table>_<Column>"
		// as a constraint, and that constraint is the column's only unique index.
		`CREATE TABLE "LegacyConstraintOnlyUniques" ("ID" bigserial PRIMARY KEY, "Code" text)`,
		`ALTER TABLE "LegacyConstraintOnlyUniques" ADD CONSTRAINT "idx_LegacyConstraintOnlyUniques_Code" UNIQUE ("Code")`,
	)
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS "LegacyCustomUniques", "LegacyAddedUniques", "LegacyConstraintOnlyUniques"`)
	})

	ddl := recordLegacyUniqueDDL(t, db)
	models := []interface{}{&customUniqueIndexModel{}, &defaultUniqueIndexModel{}, &constraintOnlyUniqueIndexModel{}}
	if err := db.Session(&gorm.Session{DryRun: true}).AutoMigrate(models...); err != nil {
		t.Fatalf("dry-run AutoMigrate: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AutoMigrate(models...); err != nil {
			t.Fatalf("AutoMigrate pass %d: %v", i+1, err)
		}
	}
	if len(*ddl) != 0 {
		t.Fatalf("planning and migrating emitted DDL: %q", *ddl)
	}

	var constraints []string
	if err := db.Raw(`SELECT conname FROM pg_constraint WHERE contype = 'u' AND conrelid IN ('"LegacyCustomUniques"'::regclass, '"LegacyAddedUniques"'::regclass, '"LegacyConstraintOnlyUniques"'::regclass) ORDER BY conname`).Scan(&constraints).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"LegacyAddedUniques_ExternalName_key", "idx_LegacyConstraintOnlyUniques_Code", "idx_LegacyCustomUniques_OwnerId"}
	if strings.Join(constraints, ",") != strings.Join(want, ",") {
		t.Fatalf("unique constraints = %v, want %v", constraints, want)
	}
}

// MigrateColumnUnique gets a table name instead of a model when callers use
// the Migrator directly. The field still carries its schema, so a covered
// column keeps its legacy constraint there too.
func TestMigrateColumnUnique_TableNameValueKeepsLegacyConstraint(t *testing.T) {
	db := openSequenceTestDB(t)
	execAll(t, db,
		`DROP TABLE IF EXISTS "LegacyCustomUniques"`,
		`CREATE TABLE "LegacyCustomUniques" ("ID" bigserial PRIMARY KEY, "OwnerId" text)`,
		`CREATE UNIQUE INDEX "idx_legacy_owner" ON "LegacyCustomUniques" ("OwnerId")`,
		`ALTER TABLE "LegacyCustomUniques" ADD CONSTRAINT "idx_LegacyCustomUniques_OwnerId" UNIQUE ("OwnerId")`,
	)
	t.Cleanup(func() { db.Exec(`DROP TABLE IF EXISTS "LegacyCustomUniques"`) })
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&customUniqueIndexModel{}); err != nil {
		t.Fatal(err)
	}
	field := stmt.Schema.LookUpField("OwnerId")
	columnTypes, err := db.Migrator().ColumnTypes(&customUniqueIndexModel{})
	if err != nil {
		t.Fatal(err)
	}

	ddl := recordLegacyUniqueDDL(t, db)
	for _, columnType := range columnTypes {
		if columnType.Name() != field.DBName {
			continue
		}
		if err := db.Migrator().(Migrator).MigrateColumnUnique("LegacyCustomUniques", field, columnType); err != nil {
			t.Fatalf("MigrateColumnUnique(table name): %v", err)
		}
	}
	if len(*ddl) != 0 {
		t.Fatalf("MigrateColumnUnique(table name) emitted DDL: %q", *ddl)
	}
}

// gormDropsUniqueConstraints reports whether this gorm version's MigrateColumn
// calls MigrateColumnUnique. v1.25.7 made that call, and in the same release
// stopped marking a field that is the only column of a unique index Unique;
// v1.25.6 already defines MigrateColumnUnique but never calls it.
func gormDropsUniqueConstraints(t *testing.T) bool {
	t.Helper()
	return !parseSchema(t, &defaultUniqueIndexModel{}).FieldsByDBName["ExternalName"].Unique
}

// The override only keeps constraints that a plain single-column unique index
// makes redundant. A legacy constraint on a column of a composite or partial
// unique index disagrees with the model, so the plan must still show the base
// migrator's DROP rather than the migration keeping it silently.
func TestAutoMigrate_PlansDropForLegacyUniqueConstraintsTheModelDoesNotCover(t *testing.T) {
	if !gormDropsUniqueConstraints(t) {
		t.Skip("gorm before v1.25.7 never calls MigrateColumnUnique, so it never drops unique constraints")
	}
	db := openSequenceTestDB(t)
	execAll(t, db,
		`DROP TABLE IF EXISTS "LegacyCompositeUniques", "LegacyPartialUniques"`,
		`CREATE TABLE "LegacyCompositeUniques" ("ID" bigserial PRIMARY KEY, "AccountId" text, "Name" text)`,
		`CREATE UNIQUE INDEX "idx_legacy_account_name" ON "LegacyCompositeUniques" ("AccountId", "Name")`,
		`ALTER TABLE "LegacyCompositeUniques" ADD CONSTRAINT "LegacyCompositeUniques_AccountId_key" UNIQUE ("AccountId")`,
		`CREATE TABLE "LegacyPartialUniques" ("ID" bigserial PRIMARY KEY)`,
		`ALTER TABLE "LegacyPartialUniques" ADD "SsoDomain" text UNIQUE`,
		`CREATE UNIQUE INDEX "idx_legacy_sso_domain" ON "LegacyPartialUniques" ("SsoDomain") WHERE "SsoDomain" <> ''`,
	)
	t.Cleanup(func() { db.Exec(`DROP TABLE IF EXISTS "LegacyCompositeUniques", "LegacyPartialUniques"`) })

	ddl := recordLegacyUniqueDDL(t, db)
	if err := db.Session(&gorm.Session{DryRun: true}).AutoMigrate(&compositeUniqueIndexModel{}, &partialLegacyUniqueModel{}); err != nil {
		t.Fatalf("dry-run AutoMigrate: %v", err)
	}
	want := []string{
		`DROP CONSTRAINT "uni_LegacyCompositeUniques_AccountId"`,
		`DROP CONSTRAINT "uni_LegacyPartialUniques_SsoDomain"`,
	}
	if len(*ddl) != len(want) {
		t.Fatalf("dry-run DDL = %q, want one statement each containing %q", *ddl, want)
	}
	for i, fragment := range want {
		if !strings.Contains((*ddl)[i], fragment) {
			t.Fatalf("dry-run DDL = %q, want one statement each containing %q", *ddl, want)
		}
	}
}

func execAll(t *testing.T, db *gorm.DB, statements ...string) {
	t.Helper()
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// recordLegacyUniqueDDL collects the DDL db runs, or prints in dry-run mode,
// from now on.
func recordLegacyUniqueDDL(t *testing.T, db *gorm.DB) *[]string {
	t.Helper()
	var ddl []string
	if err := db.Callback().Raw().Before("gorm:raw").Register("test:record_legacy_unique_ddl", func(tx *gorm.DB) {
		statement := strings.ToUpper(strings.TrimSpace(tx.Statement.SQL.String()))
		for _, prefix := range []string{"CREATE ", "ALTER ", "DROP ", "COMMENT "} {
			if strings.HasPrefix(statement, prefix) {
				ddl = append(ddl, tx.Statement.SQL.String())
				return
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	return &ddl
}
