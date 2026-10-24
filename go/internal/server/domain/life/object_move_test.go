package life_test

import (
	"testing"
)

// LifePacket.SendLifeWorldObjectPositionSave names the object only in the
// before-position; the after-position carries X, Y and Rotate.
func TestMoveAcceptsAnAfterPositionWithoutTheObjectID(t *testing.T) {
	g := scenario(t)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{304}] = proto(5, 304, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{304}] = proto(4, 304, 11, 5)
	g.call("/LifeWorldChoice", proto(2, 91))
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 5, 6)))
	after := child(proto(1, 1), 2, proto(1, 2, 2, 3, 3, 1))
	g.call("/LifeWorldObjectPositionSave", child(nil, 2, child(child(nil, 1, placement(304, 5, 6)), 2, after)))
	g.call("/LifeWorldObjectUnplaceSave", child(nil, 2, placement(304, 2, 3)))
}

// LifeWorldObjectPositionDeltaSave's dx/dy are world-cell offsets. Chunk 1
// sits left of chunk 2 on the farm grid, so moving an object four cells right
// from local x 8 lands it on chunk 2 at local x 2, as the client shows it.
func TestDeltaMoveCarriesObjectsAcrossChunks(t *testing.T) {
	g := scenario(t)
	g.d.Tables["LifeWorldTable"][[3]uint64{91}] = proto(4, 91, 1, 1, 1, 2)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{304}] = proto(5, 304, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{304}] = proto(4, 304, 11, 5)
	g.call("/LifeWorldChoice", proto(2, 91))
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 8, 6)))
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(304, 2, 2)))
	delta := proto(2, 8, 3, 0) // sint32 zigzag: dx=4, dy=0
	delta = child(delta, 4, proto(1, 1, 2, 8, 3, 6, 4, 304))
	delta = child(delta, 4, proto(1, 1, 2, 2, 3, 2, 4, 304))
	g.call("/LifeWorldObjectPositionDeltaSave", delta)
	g.call("/LifeWorldObjectUnplaceSave", child(nil, 2, child(proto(1, 2), 2, proto(6, 304, 1, 2, 2, 6))))
	g.call("/LifeWorldObjectUnplaceSave", child(nil, 2, placement(304, 6, 2)))
}
