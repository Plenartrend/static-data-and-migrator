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
		{"Amt. Präs.", 19, false},
		{"MdBR", 19, false},
		{"MdBR", 18, false},
		{"MdB", 16, true},
	}

	if len(roles) != len(expectedRoles) {
		t.Errorf("Expected %d roles, got %d", len(expectedRoles), len(roles))
	}

	// Compare roles and expectedRoles as unordered sets

	// Create maps to count and conveniently find expected and actual roles
	type roleKey struct {
		Name           string
		ElectionPeriod int
		HasGroup       bool
	}
	expectedRoleMap := make(map[roleKey]struct{})
	for _, expected := range expectedRoles {
		expectedRoleMap[roleKey{expected.Name, expected.ElectionPeriod, expected.HasGroup}] = struct{}{}
	}
	actualRoleMap := make(map[roleKey]struct{})
	for _, role := range roles {
		hasGroup := role.GroupID.Valid && role.GroupID.Int64 != 0
		k := roleKey{
			Name:           role.RoleName.String,
			ElectionPeriod: int(role.ElectionPeriod.Int64),
			HasGroup:       hasGroup,
		}
		actualRoleMap[k] = struct{}{}

		// Still validate the names for every role (should all match)
		if role.LastName != "Ramelow" {
			t.Errorf("For role %+v: expected last name 'Ramelow', got %q", k, role.LastName)
		}
		if role.FirstName != "Bodo" {
			t.Errorf("For role %+v: expected first name 'Bodo', got %q", k, role.FirstName)
		}
	}

	// Report any missing roles
	for rk := range expectedRoleMap {
		if _, found := actualRoleMap[rk]; !found {
			t.Errorf("Missing expected role: %+v", rk)
		}
	}
	// Report any unexpected roles
	for rk := range actualRoleMap {
		if _, found := expectedRoleMap[rk]; !found {
			t.Errorf("Unexpected role found in DB: %+v", rk)
		}
	}
}
