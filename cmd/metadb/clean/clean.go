package clean

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/metadb-project/metadb/cmd/internal/eout"
	"github.com/metadb-project/metadb/cmd/metadb/catalog"
	"github.com/metadb-project/metadb/cmd/metadb/dbx"
	"github.com/metadb-project/metadb/cmd/metadb/option"
	"github.com/metadb-project/metadb/cmd/metadb/util"
)

func Clean(opt *option.Clean) error {
	// Validate options
	if !opt.Force {
		// Ask for confirmation
		_, _ = fmt.Fprintf(os.Stderr, "Remove old data for data source %q? ", opt.Source)
		var confirm string
		_, err := fmt.Scanln(&confirm)
		if err != nil || (confirm != "y" && confirm != "Y" && strings.ToUpper(confirm) != "YES") {
			return nil
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	db, err := util.ReadConfigDatabase(opt.Datadir)
	if err != nil {
		return err
	}
	dc, err := db.Connect()
	if err != nil {
		return err
	}
	defer dbx.Close(dc)
	exists, err := sourceExists(dc, opt.Source)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("data source %q does not exist", opt.Source)
	}
	// Get list of tables
	cat, err := catalog.Initialize(db)
	if err != nil {
		return err
	}
	tables := cat.AllTables()
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].String() < tables[j].String()
	})
	for _, t := range tables {
		eout.Info("cleaning: %s", t.String())
		mainTable := t.MainSQL()
		q := "VACUUM ANALYZE " + mainTable
		if _, err := dc.Exec(context.TODO(), q); err != nil {
			return err
		}
		q = "UPDATE " + mainTable + " SET __cf=TRUE,__end='" + now + "',__current=FALSE " +
			"WHERE NOT __cf AND __current AND __source='" + opt.Source + "'"
		if _, err := dc.Exec(context.TODO(), q); err != nil {
			return err
		}
		// Any non-current historical data can be set to __cf=TRUE.
		q = "UPDATE " + mainTable + " SET __cf=TRUE WHERE NOT __cf AND __source='" + opt.Source +
			"'"
		if _, err := dc.Exec(context.TODO(), q); err != nil {
			return err
		}
		q = "VACUUM ANALYZE " + mainTable
		if _, err := dc.Exec(context.TODO(), q); err != nil {
			return err
		}
	}
	eout.Info("completed clean")
	return nil
}

func sourceExists(dc *pgx.Conn, sourceName string) (bool, error) {
	q := "SELECT 1 FROM metadb.source WHERE name=$1"
	var i int64
	err := dc.QueryRow(context.TODO(), q, sourceName).Scan(&i)
	switch {
	case err == pgx.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}
