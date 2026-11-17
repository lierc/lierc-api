package db_test

import (
	"context"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

// failFirstDelete makes the first DELETE of a connection fail as a lost
// deadlock (40P01) would, and lets later ones through.
func failFirstDelete(t *testing.T, exec func(string)) {
	t.Helper()
	exec(`CREATE SEQUENCE delete_tries`)
	exec(`CREATE FUNCTION lose_first_deadlock() RETURNS trigger AS $$
	  BEGIN
	    IF nextval('delete_tries') = 1 THEN
	      RAISE deadlock_detected USING MESSAGE = 'lost a deadlock';
	    END IF;
	    RETURN OLD;
	  END $$ LANGUAGE plpgsql`)
	exec(`CREATE TRIGGER lose_first_deadlock BEFORE DELETE ON connections FOR EACH ROW EXECUTE FUNCTION lose_first_deadlock()`)
}

// The app's delete cascades through the connection's logs, so it can lose
// a deadlock to the logger's inserts; it runs again instead of failing.
func TestDeleteConnectionRetriesDeadlock(t *testing.T) {
	ctx := context.Background()
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", "{}")
	failFirstDelete(t, func(sql string) {
		if _, err := d.Pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	})
	if err := d.DeleteConnection(ctx, "c1"); err != nil {
		t.Fatalf("DeleteConnection = %v", err)
	}
	var n int
	d.Pool.QueryRow(ctx, `SELECT count(*) FROM connections WHERE id = 'c1'`).Scan(&n)
	if n != 0 {
		t.Error("connection still there")
	}
}
