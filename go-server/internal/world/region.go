package world

// Region math (E9b extraction of the main.go regionOf/clientInterested
// section). Pure tile math; the 9-region interest SETS stay in package main
// (surroundingRegions delegates to internal/worldmap and playerConn.regions
// is read by frozen files).

// RegionOf maps a tile to its region id:
// (y/mapDivisionSize)*sideLen + (x/mapDivisionSize). Returns 0 while the
// world is unloaded (sideLen <= 0), mirroring main.go regionOf.
func RegionOf(x, y, sideLen, divSize int) int {
	if sideLen <= 0 {
		return 0
	}
	return (y/divSize)*sideLen + (x / divSize)
}

// InterestHit reports whether the entity region is in the client's
// surrounding-regions interest set (main.go clientInterested core).
func InterestHit(entityRegion int, regions []int) bool {
	for _, r := range regions {
		if r == entityRegion {
			return true
		}
	}
	return false
}
