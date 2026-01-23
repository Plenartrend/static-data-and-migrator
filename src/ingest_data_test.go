package main

import (
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func TestPersonRolesForBodoRamelow(t *testing.T) {
	err := godotenv.Load("../.env.test") // Load .env from project root
	if err != nil {
		t.Fatalf("Failed to load .env.test: %v", err)
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	var roles []Role
	err = db.Select(&roles, `
		SELECT * FROM roles 
		WHERE person_id = '8174' 
		ORDER BY election_period DESC
	`)
	if err != nil {
		t.Fatalf("Failed to query roles: %v", err)
	}

	// Expected roles for Bodo Ramelow (person 8174)
	expectedRoles := []struct {
		Name           string
		ElectionPeriod int
		HasGroup       bool
	}{
		{"MdB", 21, true},
		{"Bundestagsvizepräs.", 21, false},
		{"MdBR", 20, false},
		{"Bundesratspräs.", 20, false},
		{"Bundestagsvizepräs.", 20, false},
		{"Amt. Präs.", 19, false},
		{"Bundestagsvizepräs.", 19, false},
		{"MdBR", 19, false},
		{"Bundestagsvizepräs.", 18, false},
		{"MdBR", 18, false},
		{"Bundestagsvizepräs.", 16, false},
		{"MdB", 16, true},
	}

	if len(roles) != len(expectedRoles) {
		t.Errorf("Expected %d roles, got %d", len(expectedRoles), len(roles))
	}

	for i, expected := range expectedRoles {
		if i >= len(roles) {
			t.Errorf("Missing role at index %d: expected %s (period %d)", i, expected.Name, expected.ElectionPeriod)
			continue
		}

		role := roles[i]

		if role.RoleName.String != expected.Name {
			t.Errorf("Role %d: expected name %q, got %q", i, expected.Name, role.RoleName.String)
		}

		if !role.ElectionPeriod.Valid || int(role.ElectionPeriod.Int64) != expected.ElectionPeriod {
			t.Errorf("Role %d: expected election period %d, got %d", i, expected.ElectionPeriod, role.ElectionPeriod.Int64)
		}

		hasGroup := role.GroupID.Valid && role.GroupID.Int64 != 0
		if hasGroup != expected.HasGroup {
			t.Errorf("Role %d: expected hasGroup=%v, got hasGroup=%v (groupID=%d)", i, expected.HasGroup, hasGroup, role.GroupID.Int64)
		}

		if role.LastName != "Ramelow" {
			t.Errorf("Role %d: expected last name 'Ramelow', got %q", i, role.LastName)
		}

		if role.FirstName != "Bodo" {
			t.Errorf("Role %d: expected first name 'Bodo', got %q", i, role.FirstName)
		}
	}
}
