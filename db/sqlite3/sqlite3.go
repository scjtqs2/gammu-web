package sqlite3

import (
	"database/sql"
	"fmt"
	"os"
	"path"

	_ "github.com/logoove/sqlite" // not use cgo
	log "github.com/sirupsen/logrus"
)

type Database struct {
	db *sql.DB
}

type Error struct {
	Desc string
	E    any
}

func (e Error) Error() string {
	return fmt.Sprintf("[%s] %s", e.Desc, e.E)
}

var __CREATESQL1 = `CREATE TABLE if not exists messages
(id TEXT PRIMARY KEY NOT NULL,
self_number TEXT NOT NULL,
number TEXT NOT NULL,
text TEXT NOT NULL,
sent INT NOT NULL,
time DATETIME NOT NULL);`

var __CREATESQL2 = `CREATE TABLE if not exists abstract
(id TEXT PRIMARY KEY NOT NULL,
self_number TEXT NOT NULL,
number TEXT NOT NULL,
text TEXT NOT NULL,
time DATETIME NOT NULL);`

func (s *Database) Init() {
	if e := s.Exec(__CREATESQL1); e != nil {
		log.Fatalf("CreateTable1 %v", e)
	}

	if e := s.Exec(__CREATESQL2); e != nil {
		log.Fatalf("CreateTable2 %v", e)
	}
}

func (s *Database) Open() error {
	dbPath := path.Join("data", "sqlite3")
	if os.Getenv("DB_PATH") != "" {
		dbPath = os.Getenv("DB_PATH")
	}
	_ = os.MkdirAll(dbPath, 0755)
	if os.Getenv("DB_NAME") != "" {
		dbPath = path.Join(dbPath, os.Getenv("DB_NAME"))
	} else {
		dbPath += "/gammu.db"
	}

	var err error
	s.db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return Error{"Sqlite3Open", err}
	}
	_, err = s.db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		return Error{"Sqlite3Exec", err}
	}
	return nil
}

func (s *Database) Query(str string) *sql.Rows {
	if rows, err := s.db.Query(str); err != nil {
		log.Errorf("Sqlite3Query %v", err)
		return &sql.Rows{}
	} else {
		return rows
	}
}

func (s *Database) Insert(query string, data []interface{}) {
	stmt, err := s.db.Prepare(query)
	if err != nil {
		log.Errorf("Sqlite3Prepare %v", err)
	}

	_, err = stmt.Exec(data...)
	if err != nil {
		log.Errorf("Sqlite3Insert %v", err)
	}
}

func (s *Database) Exec(query string) error {
	_, e := s.db.Exec(query)
	return e
}
