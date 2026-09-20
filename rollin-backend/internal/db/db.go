// Package db owns database connection setup. Schema evolution lives in the migrations
// subpackage; this file only opens the pool with the UTC/time-zone guarantees the data
// model requires (05-data-model.md: DATETIME stores UTC, DSN time_zone='+00:00').
package db

import (
	"fmt"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// Options carries the connection parameters (kept free of the config package so the
// migration command and tests can build it directly).
type Options struct {
	User            string
	Password        string
	Host            string
	Port            string
	Database        string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Open connects to MySQL and configures the pool. Time handling is fixed: parseTime on,
// session time_zone +00:00, Go side UTC — timestamps round-trip as UTC everywhere.
func Open(opts Options) (*gorm.DB, error) {
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 20
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = 10
	}
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = 30 * time.Minute
	}
	dsn := (&mysqlDriver.Config{
		User:      opts.User,
		Passwd:    opts.Password,
		Net:       "tcp",
		Addr:      fmt.Sprintf("%s:%s", opts.Host, opts.Port),
		DBName:    opts.Database,
		ParseTime: true,
		Loc:       time.UTC,
		Params:    map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"},
	}).FormatDSN()
	handle, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := handle.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)
	return handle, nil
}
