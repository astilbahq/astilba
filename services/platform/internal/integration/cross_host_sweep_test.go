package integration

// coordinatedScheduleSweep queues the coordinated schedule sweep by its River kind, so the
// cross-host journey can run it at the moments it chooses.
type coordinatedScheduleSweep struct{}

func (coordinatedScheduleSweep) Kind() string { return "platform_coordinated_schedule_v1" }
