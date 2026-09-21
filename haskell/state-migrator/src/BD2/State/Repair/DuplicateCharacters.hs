{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.DuplicateCharacters
  ( repairDuplicateCharacters
  ) where

import BD2.State.Domain
import Data.Foldable (toList)
import qualified Data.Map.Strict as Map
import Data.Maybe (mapMaybe)
import Data.Word (Word64)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2
import qualified Proto.Bd2.State.V2.State_Fields as V2Fields

-- | Port of CollectionStore.AttachBaseCharacters as a total pure transform.
-- Base characters are authoritative. Repeated acquired characters are
-- removed by design ID; all live cross-state owner references are redirected,
-- while historical grant character lists drop removed instance identities.
repairDuplicateCharacters :: V2.Snapshot -> Either [Problem] (V2.Snapshot, [CharacterRedirect])
repairDuplicateCharacters snapshot = do
  let roster = snapshot ^. V2Fields.roster
      records = toList (roster ^. V2Fields.characters)
      base = filter ((== V2.CHARACTER_ORIGIN_BASE) . (^. V2Fields.origin)) records
      acquired = filter ((== V2.CHARACTER_ORIGIN_ACQUIRED) . (^. V2Fields.origin)) records
  canonical <- baseCanonical base
  let (keptAcquired, redirects) = acquiredCanonical canonical acquired
      redirectMap = Map.fromList [(redirectFromIndex r, redirectToIndex r) | r <- redirects]
      redirect index = Map.findWithDefault index index redirectMap
      repairCostume costume = costume & V1Fields.useChar .~ redirect (costume ^. V1Fields.useChar)
      repairSlot slot = slot & V1Fields.characterIndex .~ redirect (slot ^. V1Fields.characterIndex)
      repairEquipment entry = entry & V1Fields.useChar .~ redirect (entry ^. V1Fields.useChar)
      repairGrant grant = grant & V1Fields.characterIndices .~ mapMaybe (keepHistorical redirectMap) (toList (grant ^. V1Fields.characterIndices))
      deck = snapshot ^. V2Fields.deck
      equipment = snapshot ^. V2Fields.equipment
      ledger = snapshot ^. V2Fields.collection
      repaired =
        snapshot
          & V2Fields.roster . V2Fields.characters .~ (base <> keptAcquired)
          & V2Fields.roster . V2Fields.costumes .~ map repairCostume (toList (roster ^. V2Fields.costumes))
          & V2Fields.deck . V1Fields.battle .~ map repairSlot (toList (deck ^. V1Fields.battle))
          & V2Fields.deck . V1Fields.field .~ map repairSlot (toList (deck ^. V1Fields.field))
          & V2Fields.equipment . V1Fields.equipment .~ map repairEquipment (toList (equipment ^. V1Fields.equipment))
          & V2Fields.collection . V2Fields.grants .~ map repairGrant (toList (ledger ^. V2Fields.grants))
  pure (repaired, redirects)

baseCanonical :: [V2.CharacterRecord] -> Either [Problem] (Map.Map Word64 Word64)
baseCanonical = go Map.empty
  where
    go canonical [] = Right canonical
    go canonical (record : rest) =
      let character = record ^. V2Fields.character
          designId = character ^. V1Fields.id
          index = character ^. V1Fields.inventoryIndex
       in case Map.lookup designId canonical of
            Just existing -> Left [Problem "repair.duplicate_base_character" Error ["roster", "characters"] "base character design is not unique" [designId, existing, index]]
            Nothing -> go (Map.insert designId index canonical) rest

acquiredCanonical :: Map.Map Word64 Word64 -> [V2.CharacterRecord] -> ([V2.CharacterRecord], [CharacterRedirect])
acquiredCanonical initial = go initial [] []
  where
    go _ kept redirects [] = (reverse kept, reverse redirects)
    go canonical kept redirects (record : rest) =
      let character = record ^. V2Fields.character
          designId = character ^. V1Fields.id
          index = character ^. V1Fields.inventoryIndex
       in case Map.lookup designId canonical of
            Just existing -> go canonical kept (CharacterRedirect designId index existing : redirects) rest
            Nothing -> go (Map.insert designId index canonical) (record : kept) redirects rest

keepHistorical :: Map.Map Word64 Word64 -> Word64 -> Maybe Word64
keepHistorical redirects index
  | Map.member index redirects = Nothing
  | otherwise = Just index
