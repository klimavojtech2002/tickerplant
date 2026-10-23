package source

// ChaosPeriod is the length, in steps, of one repeat of the chaos schedule.
const ChaosPeriod = 300

// chaosJitter is how many steps a fault may slide from its base offset inside its window.
const chaosJitter = 20

// chaosPlan lists the faults of one period and the base offset of each. A fault lands at its
// base plus a jitter below chaosJitter, so the windows are [40,59], [100,119], [150,169],
// [200,219], [240,259] and [270,289]. The closest two faults can be is 11 steps (a cross at its
// latest, a coalesce at its earliest), and the engine needs only two or three: a resync is
// synchronous and the synthetic snapshot is the truth at the current sequence, so after any of
// these faults the engine is caught up at once. The first window is past step 40, when the
// engine is bound, and the last ends at 289, so every period ends clean.
var chaosPlan = [...]struct {
	kind Fault
	base int
}{
	{FaultGap, 40},
	{FaultReorder, 100},
	{FaultDuplicate, 150},
	{FaultDisconnect, 200},
	{FaultCross, 240},
	{FaultCoalesce, 270},
}

// ChaosSchedule returns a fault schedule for Config.FaultAt: in every period of ChaosPeriod steps
// each of the six fault kinds happens once, at a step that depends only on the seed. It is a pure
// function of (seed, step), with no state, clock or global rand, so the same seed always gives the
// same faults and a run of any length needs no table. A negative step has no fault.
func ChaosSchedule(seed int64) func(step int) Fault {
	return func(step int) Fault {
		if step < 0 {
			return FaultNone
		}
		cycle, r := uint64(step/ChaosPeriod), step%ChaosPeriod
		for _, p := range chaosPlan {
			if r == p.base+chaosOffset(seed, cycle, p.kind) {
				return p.kind
			}
		}
		return FaultNone
	}
}

// chaosOffset is the jitter of one fault in one period, from 0 to chaosJitter-1. The seed, the period
// and the kind are mixed one after the other (not XORed), so different (seed, period) pairs do not
// collide. The kind is the Fault constant, so the schedule depends on the order of those constants.
func chaosOffset(seed int64, cycle uint64, kind Fault) int {
	h := splitmix64(splitmix64(splitmix64(uint64(seed))+cycle) + uint64(kind))
	return int(h % chaosJitter)
}

// splitmix64 is the SplitMix64 finalizer: a fast, well-mixed 64-bit hash. The state moves by the
// golden-ratio constant first, so zero is not a fixed point.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
