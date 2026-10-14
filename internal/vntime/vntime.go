// Package vntime fixes the time zone of the whole API to Vietnam time (UTC+7): times without a
// zone are read in it, time searches are read in it, and times in responses are written in it.
package vntime

import "time"

// Loc is UTC+7 (Asia/Ho_Chi_Minh has no daylight saving time).
var Loc = time.FixedZone("ICT", 7*60*60)
