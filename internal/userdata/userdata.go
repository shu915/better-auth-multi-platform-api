// Package userdata declares what happens to the data of each table when its user deletes the
// account. It holds no code that deletes anything: DELETE /me removes the profiles row (the
// root), and each table's foreign key to profiles(user_id) decides the rest. The tests check
// that every table with a user_id column is declared here and that its foreign key matches.
//
// Deleting everything is not always right (a comment may stay after its author leaves), so a
// new table must say which of the policies below it follows, and why.
package userdata

// Policy says what happens to a table's rows when the user deletes the account.
type Policy string

const (
	// Delete removes the rows with the user. The table's user_id references profiles(user_id)
	// ON DELETE CASCADE.
	Delete Policy = "delete"
	// Anonymize keeps the rows but detaches them from the user, so the content can show
	// "a deleted user". user_id is nullable and references profiles(user_id) ON DELETE SET NULL.
	Anonymize Policy = "anonymize"
	// Retain keeps the rows as they are, for example for a legal duty to keep records.
	// user_id is just an id with no foreign key, so it outlives the user.
	Retain Policy = "retain"
)

// Entry is the decision for one table.
type Entry struct {
	Policy Policy
	Reason string // why this policy: what the data is and why it goes, is detached, or stays
}

// Root is the table whose row deletion starts everything else. Its own user_id needs no foreign key.
const Root = "profiles"

// Tables lists every table in the public schema that has a user_id column. Add a table here in
// the same change that creates it.
var Tables = map[string]Entry{
	Root: {Policy: Delete, Reason: "the bio is the user's own data and is of no use without them"},
}
