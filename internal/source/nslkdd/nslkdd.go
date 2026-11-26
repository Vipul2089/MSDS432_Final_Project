// Package nslkdd maps NSL-KDD rows, as written by loadgen, to events.
package nslkdd

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	"obsfabric/internal/event"
)

const (
	Source = "nslkdd"

	prefixColumns = 3 // event_time, src_ip, dst_ip added by loadgen
	dataColumns   = 43
)

// Positions of the NSL-KDD columns we use, counted from the first NSL-KDD column.
const (
	colDuration = 0
	colProtocol = 1
	colService  = 2
	colSrcBytes = 4
	colDstBytes = 5
	colLabel    = 41
)

// Parse maps one line to an event. ID, partition, batch and observed time are
// filled in by the caller. Invalid IP addresses or byte counts are dropped
// rather than rejecting the whole record.
func Parse(line string) (event.Event, error) {
	f := strings.Split(line, ",")
	if len(f) != prefixColumns+dataColumns {
		return event.Event{}, &event.RejectError{
			Reason: event.ReasonParse,
			Detail: "expected " + strconv.Itoa(prefixColumns+dataColumns) + " columns, got " + strconv.Itoa(len(f)),
		}
	}
	if f[0] == "" {
		return event.Event{}, &event.RejectError{Reason: event.ReasonMissingField, Detail: "event_time"}
	}
	ts, err := time.Parse(time.RFC3339Nano, f[0])
	if err != nil {
		return event.Event{}, &event.RejectError{Reason: event.ReasonBadTimestamp, Detail: f[0]}
	}

	d := f[prefixColumns:]
	label := d[colLabel]
	if label == "" {
		return event.Event{}, &event.RejectError{Reason: event.ReasonMissingField, Detail: "label"}
	}
	category := Category(label)

	e := event.Event{
		EventTime:     ts.UTC(),
		Source:        Source,
		EventType:     "connection",
		Category:      category,
		Severity:      severity[category],
		SrcIP:         validIP(f[1]),
		DstIP:         validIP(f[2]),
		Protocol:      strings.ToLower(d[colProtocol]),
		Service:       d[colService],
		Label:         label,
		RawMessage:    strings.Join(d, ","),
		IsSynthetic:   true,
		SchemaVersion: event.SchemaVersion,
	}

	src, err1 := strconv.ParseInt(d[colSrcBytes], 10, 64)
	dst, err2 := strconv.ParseInt(d[colDstBytes], 10, 64)
	if err1 == nil && err2 == nil && src >= 0 && dst >= 0 {
		b := src + dst
		e.Bytes = &b
	}
	if dur, err := strconv.ParseFloat(d[colDuration], 64); err == nil && dur >= 0 {
		e.DurationS = &dur
	}
	return e, nil
}

func validIP(s string) string {
	if _, err := netip.ParseAddr(s); err != nil {
		return ""
	}
	return s
}

// Category maps an NSL-KDD label to its attack category. Labels we do not
// recognise are treated as normal traffic (LLD-001 §3.2).
func Category(label string) string {
	if c, ok := categories[label]; ok {
		return c
	}
	return "normal"
}

var severity = map[string]string{
	"normal": "INFO",
	"Probe":  "WARN",
	"DoS":    "ERROR",
	"R2L":    "CRITICAL",
	"U2R":    "CRITICAL",
}

// categories covers every label in KDDTrain+ and KDDTest+.
var categories = map[string]string{
	"normal": "normal",

	"back": "DoS", "land": "DoS", "neptune": "DoS", "pod": "DoS", "smurf": "DoS",
	"teardrop": "DoS", "apache2": "DoS", "mailbomb": "DoS", "processtable": "DoS",
	"udpstorm": "DoS",

	"ipsweep": "Probe", "nmap": "Probe", "portsweep": "Probe", "satan": "Probe",
	"mscan": "Probe", "saint": "Probe",

	"ftp_write": "R2L", "guess_passwd": "R2L", "imap": "R2L", "multihop": "R2L",
	"phf": "R2L", "spy": "R2L", "warezclient": "R2L", "warezmaster": "R2L",
	"named": "R2L", "sendmail": "R2L", "snmpgetattack": "R2L", "snmpguess": "R2L",
	"xlock": "R2L", "xsnoop": "R2L", "worm": "R2L",

	"buffer_overflow": "U2R", "loadmodule": "U2R", "perl": "U2R", "rootkit": "U2R",
	"httptunnel": "U2R", "ps": "U2R", "sqlattack": "U2R", "xterm": "U2R",
}
