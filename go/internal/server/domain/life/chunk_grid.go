package life

// The farm's chunk layout, printed by tools/python/life_chunk_grid.py from
// the GroundChunkManager in pack3011-bundlepack3011_assets_p3011_map.bundle.
// GameData has no table for it. The client places chunks on this grid and
// numbers world cells gridX*chunkCells+localX (GroundChunkManager.
// GetCellAtWorldCell), so LifeWorldObjectPositionDeltaSave's world-cell
// offsets can carry an object into a neighbouring chunk.

const chunkGridWidth, chunkGridHeight = 5, 5

// chunkGrid[gridY*chunkGridWidth+gridX] is the chunk at that grid cell.
var chunkGrid = [...]uint64{
	21, 20, 19, 18, 17,
	22, 7, 8, 9, 16,
	23, 4, 5, 6, 15,
	24, 1, 2, 3, 14,
	25, 10, 11, 12, 13,
}

const chunkCells = 10

// worldCell is the world cell of a chunk's local cell.
func worldCell(chunk uint64, x, y int64) (int64, int64, bool) {
	for i, id := range chunkGrid {
		if id == chunk {
			return int64(i%chunkGridWidth)*chunkCells + x, int64(i/chunkGridWidth)*chunkCells + y, true
		}
	}
	return 0, 0, false
}

// chunkCell is the chunk and local cell of a world cell.
func chunkCell(x, y int64) (uint64, int32, int32, bool) {
	if x < 0 || y < 0 || x >= chunkGridWidth*chunkCells || y >= chunkGridHeight*chunkCells {
		return 0, 0, 0, false
	}
	return chunkGrid[y/chunkCells*chunkGridWidth+x/chunkCells], int32(x % chunkCells), int32(y % chunkCells), true
}
