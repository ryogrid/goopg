package parser_test

import (
	"testing"
	"github.com/goopg/goopg/internal/parser"
)

func TestM0097_0017_EnumDomainParsing(t *testing.T) {
	tests := []string{
		`CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`,
		`CREATE TYPE planets AS ENUM ('venus', 'earth', 'mars')`,
		`ALTER TYPE planets ADD VALUE 'uranus'`,
		`ALTER TYPE planets ADD VALUE IF NOT EXISTS 'mercury'`,
		`ALTER TYPE planets ADD VALUE 'mercury' BEFORE 'venus'`,
		`ALTER TYPE planets ADD VALUE 'neptune' AFTER 'uranus'`,
		`DROP TYPE rainbow`,
		`DROP TYPE rainbow CASCADE`,
		`CREATE DOMAIN domaindroptest int4`,
		`CREATE DOMAIN domainvarchar varchar(5)`,
		`CREATE DOMAIN domainnumeric numeric(8,2)`,
		`CREATE DOMAIN domainint4 int4`,
		`CREATE DOMAIN domaintext text`,
		`CREATE DOMAIN d_notnull AS int4 NOT NULL`,
		`DROP DOMAIN domaindroptest`,
		`DROP DOMAIN domaindroptest CASCADE`,
		`DROP DOMAIN domaindroptest RESTRICT`,
	}
	for _, sql := range tests {
		t.Run(sql[:min(60,len(sql))], func(t *testing.T) {
			_, err := parser.Parse(sql)
			if err != nil {
				t.Errorf("Parse(%q) error: %v", sql, err)
			}
		})
	}
}

func min(a, b int) int { if a < b { return a }; return b }
