//go:build upgradetest

package db

func init() {
	migrations = append(migrations, `CREATE TABLE upgrade_probe (id INTEGER PRIMARY KEY, note TEXT NOT NULL DEFAULT '')`)
}
