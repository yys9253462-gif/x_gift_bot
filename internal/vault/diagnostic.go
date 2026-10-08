package vault

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Diagnostic records are the audit trail for X read/creation attempts that
// failed. They are useful precisely when something is wrong, which is also
// when they are produced fastest: a stuck retry loop or a wrong credential can
// write several per second. Appending one row per attempt with a unique key
// therefore grows the vault without bound, and every row is loaded into memory
// by diagnostics.
//
// So keep the newest N per prefix and drop the rest. The count is bounded per
// prefix rather than globally, because a failure storm for one account must not
// evict the evidence for another.

// diagnosticKeepPerPrefix is how many records to retain per prefix. 40 covers
// a full retry budget (maxAttempts is single digit) plus room to see a trend,
// while staying small enough that the vault never grows unboundedly.
const diagnosticKeepPerPrefix = 40

// PutDiagnostic writes one bounded diagnostic record under prefix.
//
// The record keeps its own unique key (prefix + timestamp) so a burst leaves
// distinct rows an operator can read back in order, then everything beyond
// diagnosticKeepPerPrefix is deleted immediately — not by a later sweep,
// because during a failure storm there may never be one.
func (v *Vault) PutDiagnostic(prefix string, plain []byte) error {
	if prefix == "" {
		return fmt.Errorf("diagnostic prefix is required")
	}
	key := fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
	if err := v.Put(key, plain); err != nil {
		return err
	}
	// Trim by the fixed prefix, keeping the newest rows by rowid. Matching on
	// prefix + rowid means only our own diagnostics are ever considered.
	//
	// prefix goes through LIKE with an escaped wildcard, not string
	// concatenation: concatenating it into SQL would make a caller-supplied
	// prefix able to match (and delete) unrelated rows.
	if _, err := v.db.Exec(
		`DELETE FROM secrets WHERE rowid IN (
			SELECT rowid FROM secrets
			 WHERE name LIKE ? ESCAPE '!'
			 ORDER BY rowid DESC
			 LIMIT -1 OFFSET ?
		 )`,
		likePrefix(prefix), diagnosticKeepPerPrefix,
	); err != nil {
		// The record itself is already durable; failing to trim must not turn
		// a diagnostic into an error the caller has to handle.
		return nil
	}
	return nil
}

// CountDiagnostics reports how many records exist under a prefix.
func (v *Vault) CountDiagnostics(prefix string) (int, error) {
	var n int
	err := v.db.QueryRow(
		"SELECT count(*) FROM secrets WHERE name LIKE ? ESCAPE '!'", likePrefix(prefix),
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// likePrefix turns a literal prefix into a LIKE pattern that only matches it.
// % and _ are escaped so a prefix containing them cannot widen the match.
func likePrefix(prefix string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(prefix) + ":%"
}

// DiagnosticPrefix builds the key for a diagnostic of the given kind and
// subject, so callers do not hand-format keys and drift apart.
func DiagnosticPrefix(kind, subject string) string {
	return kind + ":" + subject + ":" + fmt.Sprintf("%d", time.Now().UnixNano())
}

// TrimDiagnostics deletes everything beyond the newest
// diagnosticKeepPerPrefix records under prefix.
//
// Used where a single logical event updates one fixed key in place (the
// creation audit records "attempt sent" and then "outcome"), so the write
// itself cannot trim: both writes must land on the same row. Trimming has to
// happen once at the end instead.
func (v *Vault) TrimDiagnostics(prefix string) error {
	_, err := v.db.Exec(
		`DELETE FROM secrets WHERE rowid IN (
			SELECT rowid FROM secrets
			 WHERE name LIKE ? ESCAPE '!'
			 ORDER BY rowid DESC
			 LIMIT -1 OFFSET ?
		 )`,
		likePrefix(prefix), diagnosticKeepPerPrefix,
	)
	return err
}
