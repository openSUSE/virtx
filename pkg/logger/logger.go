package logger

import (
	"fmt"
	"strings"
	"os"
	"runtime"
	"path/filepath"
	"sync"
	"time"
)

var l struct {
	m sync.Mutex
}

/* called under lock and with the caller string already set */
func doLog(caller string, format string, args ...interface{}) {
	caller = filepath.Base(caller)
	caller = strings.TrimSuffix(caller, filepath.Ext(caller))

	var now string = time.Now().UTC().Format(time.DateTime)
	var prefix string = fmt.Sprintf("%s %s: %s: ", now, "virtXD", caller)

	fmt.Fprint(os.Stderr, prefix)
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
}

func Log(format string, args ...interface{}) {
	l.m.Lock()
	defer l.m.Unlock()

	var caller string
	_, caller, _, _ = runtime.Caller(1)
	doLog(caller, format, args...)
}

func Fatal(format string, args ...interface{}) {
	l.m.Lock()
	defer l.m.Unlock()

	var caller string
	_, caller, _, _ = runtime.Caller(1)
	doLog(caller, format, args...)
	os.Exit(1)
}
