package mapping

import (
	"strings"
	"testing"
)

const testConfig = `version: 1
categories:
  public-restroom:
    database_category: public-bathroom
    description: Toilets
    match:
      any:
        - all:
            amenity: toilets
    exclude:
      any:
        - all:
            access: private
    defaults:
      name: سرویس بهداشتی
  worship:
    database_category: mosque
    description: Muslim worship places
    match:
      any:
        - all:
            amenity: place_of_worship
            religion: muslim
        - all:
            building: mosque
`

func TestCategoryRulesAndDatabaseMapping(t *testing.T) {
	cfg, err := Decode(strings.NewReader(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	restroom, err := cfg.Category("public-restroom")
	if err != nil {
		t.Fatal(err)
	}
	if restroom.DatabaseCategory != "public-bathroom" {
		t.Fatalf("database category = %q", restroom.DatabaseCategory)
	}
	tests := []struct {
		name string
		tags map[string]string
		want bool
	}{
		{"exact", map[string]string{"amenity": "toilets"}, true},
		{"wrong value", map[string]string{"amenity": "toilet"}, false},
		{"excluded", map[string]string{"amenity": "toilets", "access": "private"}, false},
		{"missing", map[string]string{"access": "yes"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restroom.MatchTags(tt.tags); got != tt.want {
				t.Fatalf("MatchTags(%v) = %t, want %t", tt.tags, got, tt.want)
			}
		})
	}
	worship, _ := cfg.Category("worship")
	if !worship.MatchTags(map[string]string{"amenity": "place_of_worship", "religion": "muslim"}) {
		t.Fatal("AND rule did not match")
	}
	if !worship.MatchTags(map[string]string{"building": "mosque"}) {
		t.Fatal("OR alternative did not match")
	}
	if worship.MatchTags(map[string]string{"amenity": "place_of_worship"}) {
		t.Fatal("incomplete AND rule matched")
	}
}

func TestDecodeRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name, yaml string
	}{
		{"unknown field", strings.Replace(testConfig, "description: Toilets", "descripton: Toilets", 1)},
		{"empty match", strings.Replace(testConfig, "        - all:\n            amenity: toilets", "        - all: {}", 1)},
		{"unknown category form", strings.Replace(testConfig, "public-restroom:", "Public Restroom:", 1)},
		{"missing mapping", strings.Replace(testConfig, "database_category: public-bathroom", "database_category: ''", 1)},
		{"duplicate category", testConfig + "\n  public-restroom:\n    database_category: duplicate\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(tt.yaml)); err == nil {
				t.Fatal("expected invalid configuration error")
			}
		})
	}
}

func TestUnknownCategory(t *testing.T) {
	cfg, err := Decode(strings.NewReader(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.Category("unknown"); err == nil {
		t.Fatal("unknown category should fail")
	}
}
