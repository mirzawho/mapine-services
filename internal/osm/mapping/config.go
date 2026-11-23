package mapping

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var categoryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Config struct {
	Version    int                 `yaml:"version"`
	Categories map[string]Category `yaml:"categories"`
}

type Category struct {
	DatabaseCategory string   `yaml:"database_category"`
	Description      string   `yaml:"description"`
	Match            RuleSet  `yaml:"match"`
	Exclude          RuleSet  `yaml:"exclude"`
	Defaults         Defaults `yaml:"defaults"`
}

type Defaults struct {
	Name string `yaml:"name"`
}

// RuleSet uses OR between groups. Every key/value in a group's All map must
// match, so each group is an AND expression.
type RuleSet struct {
	Any []RuleGroup `yaml:"any"`
}

type RuleGroup struct {
	All map[string]string `yaml:"all"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open category configuration %q: %w", path, err)
	}
	defer f.Close()
	cfg, err := Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode category configuration %q: %w", path, err)
	}
	return cfg, nil
}

func Decode(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("configuration must contain exactly one YAML document")
		}
		return nil, err
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported category configuration version %d (want 1)", cfg.Version)
	}
	if len(cfg.Categories) == 0 {
		return nil, errors.New("category configuration has no categories")
	}
	names := make([]string, 0, len(cfg.Categories))
	for name := range cfg.Categories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		category := cfg.Categories[name]
		if !categoryNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid category name %q", name)
		}
		if strings.TrimSpace(category.DatabaseCategory) == "" {
			return nil, fmt.Errorf("category %q has an empty database_category", name)
		}
		if strings.TrimSpace(category.Description) == "" {
			return nil, fmt.Errorf("category %q has an empty description", name)
		}
		if err := validateRules(name, "match", category.Match, true); err != nil {
			return nil, err
		}
		if err := validateRules(name, "exclude", category.Exclude, false); err != nil {
			return nil, err
		}
		if strings.ContainsRune(category.Defaults.Name, '\x00') {
			return nil, fmt.Errorf("category %q has an invalid default name", name)
		}
	}
	return &cfg, nil
}

func validateRules(categoryName, ruleName string, rules RuleSet, required bool) error {
	if required && len(rules.Any) == 0 {
		return fmt.Errorf("category %q has no %s.any rules", categoryName, ruleName)
	}
	for i, group := range rules.Any {
		if len(group.All) == 0 {
			return fmt.Errorf("category %q %s.any[%d].all must not be empty", categoryName, ruleName, i)
		}
		for key, value := range group.All {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return fmt.Errorf("category %q %s.any[%d] contains an empty tag key or value", categoryName, ruleName, i)
			}
			if strings.ContainsRune(key, '\x00') || strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("category %q %s.any[%d] contains an invalid tag", categoryName, ruleName, i)
			}
		}
	}
	return nil
}

func (c *Config) Category(name string) (Category, error) {
	category, ok := c.Categories[name]
	if !ok {
		return Category{}, fmt.Errorf("unknown category %q", name)
	}
	return category, nil
}

func (c Category) MatchTags(tags map[string]string) bool {
	return c.MatchLookup(func(key string) (string, bool) {
		value, ok := tags[key]
		return value, ok
	})
}

// MatchLookup evaluates a category against a tag lookup without requiring the
// caller to allocate a map for every decoded PBF Node.
func (c Category) MatchLookup(lookup func(string) (string, bool)) bool {
	if !matchesRuleSet(c.Match, lookup) {
		return false
	}
	return !matchesRuleSet(c.Exclude, lookup)
}

func matchesRuleSet(rules RuleSet, lookup func(string) (string, bool)) bool {
	for _, group := range rules.Any {
		matches := true
		for key, expected := range group.All {
			actual, ok := lookup(key)
			if !ok || actual != expected {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func (c Category) MatchDescription() string {
	return describeRuleSet(c.Match)
}

func describeRuleSet(rules RuleSet) string {
	groups := make([]string, 0, len(rules.Any))
	for _, group := range rules.Any {
		keys := make([]string, 0, len(group.All))
		for key := range group.All {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+"="+group.All[key])
		}
		groups = append(groups, strings.Join(parts, " AND "))
	}
	return strings.Join(groups, " OR ")
}

func (c Category) ExcludeDescription() string {
	return describeRuleSet(c.Exclude)
}
