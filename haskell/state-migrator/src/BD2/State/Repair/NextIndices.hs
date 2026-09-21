{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.NextIndices
  ( repairNextIndices
  ) where

import BD2.State.Domain
import Data.Foldable (toList)
import Data.Text (Text)
import Data.Word (Word64)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2
import qualified Proto.Bd2.State.V2.State_Fields as V2Fields

-- | Repair only stale allocator cursors; never renumber or mutate an entity.
-- Historical item issuance indices count even when the item was consumed.
-- Minimum cursors are the defaults used by the Go stores. Counter overflow is
-- rejected instead of wrapping to zero or inventing a new identity.
repairNextIndices :: V2.Snapshot -> Either [Problem] V2.Snapshot
repairNextIndices source = do
  let inventory = source ^. V2Fields.inventory
      equipment = source ^. V2Fields.equipment
      roster = source ^. V2Fields.roster
      itemIndices =
        map (^. V1Fields.inventoryIndex) (toList (inventory ^. V1Fields.items))
          <> concatMap (toList . (^. V1Fields.inventoryIndices)) (toList (inventory ^. V1Fields.grantItems))
      equipmentIndices = map (^. V1Fields.inventoryIndex) (toList (equipment ^. V1Fields.equipment))
      acquiredIndices =
        [ record ^. V2Fields.character . V1Fields.inventoryIndex
        | record <- toList (roster ^. V2Fields.characters)
        , record ^. V2Fields.origin == V2.CHARACTER_ORIGIN_ACQUIRED
        ]
      costumeIndices = map (^. V1Fields.inventoryIndex) (toList (roster ^. V2Fields.costumes))
  itemNext <- advance "inventory.next_index_overflow" ["inventory", "next_index"] 900000001 (inventory ^. V1Fields.nextIndex) itemIndices
  equipmentNext <- advance "equipment.next_index_overflow" ["equipment", "next_index"] 910000001 (equipment ^. V1Fields.nextIndex) equipmentIndices
  characterNext <- advance "roster.next_character_index_overflow" ["roster", "next_acquired_character_index"] 920000001
    (roster ^. V2Fields.nextAcquiredCharacterIndex) acquiredIndices
  costumeNext <- advance "roster.next_costume_index_overflow" ["roster", "next_costume_index"] 930000001
    (roster ^. V2Fields.nextCostumeIndex) costumeIndices
  pure $ source
    & V2Fields.inventory . V1Fields.nextIndex .~ itemNext
    & V2Fields.equipment . V1Fields.nextIndex .~ equipmentNext
    & V2Fields.roster . V2Fields.nextAcquiredCharacterIndex .~ characterNext
    & V2Fields.roster . V2Fields.nextCostumeIndex .~ costumeNext

advance :: Text -> [Text] -> Word64 -> Word64 -> [Word64] -> Either [Problem] Word64
advance code path initial current used =
  let highest = maximum (0 : used)
   in if highest == maxBound
        then Left [Problem code Error path "allocator cannot advance past the maximum uint64 index" [highest]]
        else Right (maximum [initial, current, highest + 1])
