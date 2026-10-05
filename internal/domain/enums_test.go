package domain

import "testing"

func TestParseEnvironment(t *testing.T) {
	tests := []struct {
		in     string
		want   Environment
		wantOK bool
	}{
		{"dev", EnvDev, true},
		{"staging", EnvStaging, true},
		{"prod", EnvProd, true},
		{"development", EnvDev, true},
		{"production", EnvProd, true},
		{"PROD", EnvProd, true},
		{"Dev", EnvDev, true},
		{"PRODUCTION", EnvProd, true},
		{"stage", "", false},
		{"banana", "", false},
		{"", "", false},
		{" prod", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseEnvironment(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ParseEnvironment(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// The accessors hand out copies, so a caller cannot change what is valid.
func TestEnumListsAreCopies(t *testing.T) {
	cats := ErrorCategories()
	cats[0] = "tampered"
	if ErrorCategories()[0] == "tampered" || !CategoryDatabaseTimeout.Valid() {
		t.Error("modifying the returned categories slice must not affect the package")
	}

	prios := Priorities()
	prios[0] = "tampered"
	if Priorities()[0] == "tampered" || !PriorityCritical.Valid() {
		t.Error("modifying the returned priorities slice must not affect the package")
	}

	envs := Environments()
	envs[0] = "tampered"
	if Environments()[0] == "tampered" || !EnvDev.Valid() {
		t.Error("modifying the returned environments slice must not affect the package")
	}
}

func TestEnumValid(t *testing.T) {
	for _, c := range ErrorCategories() {
		if !c.Valid() {
			t.Errorf("category %q should be valid", c)
		}
	}
	for _, p := range Priorities() {
		if !p.Valid() {
			t.Errorf("priority %q should be valid", p)
		}
	}
	for _, e := range Environments() {
		if !e.Valid() {
			t.Errorf("environment %q should be valid", e)
		}
	}
	if ErrorCategory("made_up").Valid() || Priority("urgent").Valid() || Environment("production").Valid() {
		t.Error("values outside the enums must not be valid (aliases are only resolved by ParseEnvironment)")
	}
}
