package profile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shu915/better-auth-multi-platform-api/internal/userdata"
)

// These check the rules of package userdata against a real database. They live here because
// profiles is the root table and newTestStore already provides the database (and skips without
// TEST_DATABASE_URL).

// verifyUserdata returns what is wrong between the declared policies and the tables of schema
// that have a user_id column: a table nobody declared, a declaration with no table, and a
// foreign key (or its absence) that does not match the policy.
func verifyUserdata(ctx context.Context, pool *pgxpool.Pool, schema string, tables map[string]userdata.Entry) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.table_name, c.is_nullable = 'YES'
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = $1 AND c.column_name = 'user_id'`, schema)
	if err != nil {
		return nil, err
	}
	nullable := map[string]bool{}
	for rows.Next() {
		var name string
		var isNullable bool
		if err := rows.Scan(&name, &isNullable); err != nil {
			rows.Close()
			return nil, err
		}
		nullable[name] = isNullable
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var problems []string
	for name := range tables {
		if _, ok := nullable[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s: declared, but there is no such table with a user_id column", name))
		}
	}
	for name, isNullable := range nullable {
		entry, ok := tables[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: has a user_id column but no declared policy", name))
			continue
		}
		if entry.Reason == "" {
			problems = append(problems, fmt.Sprintf("%s: the policy needs a reason", name))
		}
		if name == userdata.Root && schema == "public" {
			continue // the root needs no foreign key
		}
		// confdeltype: c = CASCADE, n = SET NULL, a = NO ACTION, r = RESTRICT, d = SET DEFAULT
		var action string
		err := pool.QueryRow(ctx, `
			SELECT c.confdeltype::text
			FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN pg_attribute a ON a.attrelid = t.oid AND a.attname = 'user_id'
			WHERE c.contype = 'f' AND n.nspname = $1 AND t.relname = $2
			  AND c.confrelid = 'public.profiles'::regclass AND c.conkey = ARRAY[a.attnum]`,
			schema, name).Scan(&action)
		hasFK := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && hasFK {
			return nil, err
		}
		switch entry.Policy {
		case userdata.Delete:
			if action != "c" {
				problems = append(problems, fmt.Sprintf("%s: policy delete needs user_id REFERENCES profiles(user_id) ON DELETE CASCADE", name))
			}
		case userdata.Anonymize:
			if action != "n" {
				problems = append(problems, fmt.Sprintf("%s: policy anonymize needs user_id REFERENCES profiles(user_id) ON DELETE SET NULL", name))
			}
			if !isNullable {
				problems = append(problems, fmt.Sprintf("%s: policy anonymize needs a nullable user_id", name))
			}
		case userdata.Retain:
			if hasFK {
				problems = append(problems, fmt.Sprintf("%s: policy retain must not have a foreign key to profiles", name))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s: unknown policy %q", name, entry.Policy))
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// The real check: the migrated database matches what userdata.Tables declares. A migration that
// adds a user_id table without declaring it there (or with a foreign key that disagrees) fails here.
func TestDeclaredPoliciesMatchTheSchema(t *testing.T) {
	s, ctx := newTestStore(t)

	problems, err := verifyUserdata(ctx, s.pool, "public", userdata.Tables)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// userdataTestSchema makes a throwaway schema for tables that stand in for future ones, so they
// never touch the real tables. Their foreign keys still point at the real public.profiles.
func userdataTestSchema(ctx context.Context, t *testing.T, s *Store) string {
	t.Helper()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := "userdata_test_" + hex.EncodeToString(suffix[:])
	mustExec(ctx, t, s, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	return schema
}

func mustExec(ctx context.Context, t *testing.T, s *Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestVerifyAcceptsTablesThatMatchTheirPolicy(t *testing.T) {
	s, ctx := newTestStore(t)
	schema := userdataTestSchema(ctx, t, s)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.notes (id serial PRIMARY KEY, user_id text NOT NULL REFERENCES public.profiles(user_id) ON DELETE CASCADE)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.comments (id serial PRIMARY KEY, user_id text REFERENCES public.profiles(user_id) ON DELETE SET NULL)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.orders (id serial PRIMARY KEY, user_id text NOT NULL)`)

	problems, err := verifyUserdata(ctx, s.pool, schema, map[string]userdata.Entry{
		"notes":    {Policy: userdata.Delete, Reason: "private to the user"},
		"comments": {Policy: userdata.Anonymize, Reason: "other people's replies depend on them"},
		"orders":   {Policy: userdata.Retain, Reason: "kept for bookkeeping"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
}

// The guard must fail when the schema and the declarations disagree; otherwise it protects nothing.
func TestVerifyCatchesMismatches(t *testing.T) {
	s, ctx := newTestStore(t)
	schema := userdataTestSchema(ctx, t, s)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.undeclared (user_id text NOT NULL)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.delete_without_fk (user_id text NOT NULL)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.delete_with_no_action (user_id text NOT NULL REFERENCES public.profiles(user_id))`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.anonymize_not_null (user_id text NOT NULL REFERENCES public.profiles(user_id) ON DELETE SET NULL)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.anonymize_cascade (user_id text REFERENCES public.profiles(user_id) ON DELETE CASCADE)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.retain_with_fk (user_id text NOT NULL REFERENCES public.profiles(user_id) ON DELETE CASCADE)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.no_reason (user_id text NOT NULL REFERENCES public.profiles(user_id) ON DELETE CASCADE)`)

	problems, err := verifyUserdata(ctx, s.pool, schema, map[string]userdata.Entry{
		"delete_without_fk":     {Policy: userdata.Delete, Reason: "r"},
		"delete_with_no_action": {Policy: userdata.Delete, Reason: "r"},
		"anonymize_not_null":    {Policy: userdata.Anonymize, Reason: "r"},
		"anonymize_cascade":     {Policy: userdata.Anonymize, Reason: "r"},
		"retain_with_fk":        {Policy: userdata.Retain, Reason: "r"},
		"no_reason":             {Policy: userdata.Delete},
		"gone":                  {Policy: userdata.Delete, Reason: "r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"undeclared: has a user_id column but no declared policy",
		"delete_without_fk: policy delete needs",
		"delete_with_no_action: policy delete needs",
		"anonymize_not_null: policy anonymize needs a nullable user_id",
		"anonymize_cascade: policy anonymize needs",
		"retain_with_fk: policy retain must not have a foreign key",
		"no_reason: the policy needs a reason",
		"gone: declared, but there is no such table",
	} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("missing problem %q in %v", want, problems)
		}
	}
	if len(problems) != 8 {
		t.Errorf("got %d problems, want 8: %v", len(problems), problems)
	}
}

// Deleting the root row does what each policy promises, in a real database.
func TestDeletingTheProfileAppliesEachPolicy(t *testing.T) {
	s, ctx := newTestStore(t)
	schema := userdataTestSchema(ctx, t, s)
	const userID = "test-userdata-behavior"
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID) })
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.notes (id serial PRIMARY KEY, user_id text NOT NULL REFERENCES public.profiles(user_id) ON DELETE CASCADE)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.comments (id serial PRIMARY KEY, user_id text REFERENCES public.profiles(user_id) ON DELETE SET NULL)`)
	mustExec(ctx, t, s, `CREATE TABLE `+schema+`.orders (id serial PRIMARY KEY, user_id text NOT NULL)`)
	if _, err := s.Upsert(ctx, userID, "bye"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"notes", "comments", "orders"} {
		mustExec(ctx, t, s, `INSERT INTO `+schema+`.`+table+` (user_id) VALUES ($1)`, userID)
	}

	if err := s.Delete(ctx, userID); err != nil {
		t.Fatal(err)
	}

	count := func(table, where string, args ...any) int {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.`+table+` WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("notes", "true"); n != 0 {
		t.Errorf("delete policy: %d notes left, want 0", n)
	}
	if n := count("comments", "user_id IS NULL"); n != 1 {
		t.Errorf("anonymize policy: %d comments without an author, want 1 kept", n)
	}
	if n := count("orders", "user_id = $1", userID); n != 1 {
		t.Errorf("retain policy: %d orders left, want 1 untouched", n)
	}
}
