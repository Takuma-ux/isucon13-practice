package main

import (
	"errors"

	"github.com/go-sql-driver/mysql"
)

func isMySQLDeadlock(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1213
	}
	return false
}
