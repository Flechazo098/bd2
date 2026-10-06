package gamedata

import (
	"database/sql"
	"testing"
)

func TestCookingRecipesReadCatalogIdentities(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec("CREATE TABLE CookingTable(id INTEGER); INSERT INTO CookingTable VALUES(503),(707)"); err != nil {
		t.Fatal(err)
	}
	design, err := loadCookingRecipeDesign(db)
	if err != nil || len(design.IDs) != 2 || !design.IDs[503] || !design.IDs[707] || design.IDs[101] {
		t.Fatalf("design=%+v err=%v", design, err)
	}
	if _, err := db.Exec("INSERT INTO CookingTable VALUES(0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCookingRecipeDesign(db); err == nil {
		t.Fatal("invalid recipe identity accepted")
	}
}
