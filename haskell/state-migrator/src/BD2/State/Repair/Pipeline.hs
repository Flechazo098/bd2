{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.Pipeline
  ( repairV1ToCurrent
  ) where

import BD2.State.Domain
import BD2.State.Migrate.V1ToV2
import BD2.State.Repair.CostumeOverflow
import BD2.State.Repair.DuplicateCharacters
import BD2.State.Repair.GachaProgress
import BD2.State.Repair.NextIndices
import BD2.State.Repair.StepUpProgress
import Data.Foldable (toList)
import qualified Data.Text as Text
import Lens.Family2 ((^.))
import qualified Proto.Bd2.State.Control.V2.Control as Control
import qualified Proto.Bd2.State.Control.V2.Control_Fields as ControlFields
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2

repairV1ToCurrent :: Control.RepairRequest -> Either [Problem] (V2.Snapshot, Bool)
repairV1ToCurrent request = do
  let context = request ^. ControlFields.context
      source = request ^. ControlFields.source
      contextVersion = context ^. ControlFields.gameDataVersion
  if Text.null contextVersion || contextVersion /= source ^. V1Fields.gameDataVersion
    then Left [Problem "repair.game_data_version" Error ["context", "game_data_version"] "context and source GameData versions differ" []]
    else pure ()
  migrated <- migrateV1ToV2ForRepair source
  (deduplicated, _) <- repairDuplicateCharacters migrated
  indexed <- repairNextIndices deduplicated
  stepped <- foldl repairStep (Right indexed) (toList (context ^. ControlFields.stepUps))
  replayed <- repairGachaProgress
    (map groupFact (toList (context ^. ControlFields.gachaGroups)))
    (map gradeFact (toList (context ^. ControlFields.costumes)))
    stepped
  overflow <- repairCostumeOverflow
    (map costumeFact (toList (context ^. ControlFields.costumes)))
    (map baseFact (toList (context ^. ControlFields.baseCostumes)))
    replayed
  case validateV2 overflow of
    [] -> Right (overflow, overflow /= migrated)
    problems -> Left problems
  where
    repairStep current fact = do
      snapshot <- current
      fst <$> repairStepUpProgress
        (StepUpFact
          (request ^. ControlFields.context . ControlFields.gameDataVersion)
          (fact ^. ControlFields.groupId)
          (toList (fact ^. ControlFields.orderedGachaIds)))
        snapshot

groupFact :: Control.GachaGroupFact -> GachaGroupFact
groupFact fact = GachaGroupFact
  (fact ^. ControlFields.gachaId)
  (fact ^. ControlFields.groupId)
  (fact ^. ControlFields.fixedId)
  (fact ^. ControlFields.pointCount)

gradeFact :: Control.CostumeFact -> CostumeGradeFact
gradeFact fact = CostumeGradeFact (fact ^. ControlFields.costumeId) (fact ^. ControlFields.grade)

costumeFact :: Control.CostumeFact -> CostumeFact
costumeFact fact = CostumeFact
  (fact ^. ControlFields.costumeId)
  (fact ^. ControlFields.maxLevel)
  (fact ^. ControlFields.overflowItemType)
  (fact ^. ControlFields.overflowItemId)
  (fact ^. ControlFields.overflowItemCount)

baseFact :: Control.BaseCostumeFact -> BaseCostumeFact
baseFact fact = BaseCostumeFact
  (fact ^. ControlFields.inventoryIndex)
  (fact ^. ControlFields.costumeId)
  (fact ^. ControlFields.level)
