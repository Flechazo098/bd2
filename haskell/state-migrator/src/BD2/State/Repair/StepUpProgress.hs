{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.StepUpProgress
  ( StepUpFact (..)
  , repairStepUpProgress
  ) where

import BD2.State.Domain
import Data.Foldable (toList)
import Data.List (find)
import qualified Data.Set as Set
import Data.Text (Text)
import qualified Data.Text as Text
import Data.Word (Word64)
import Data.ProtoLens (defMessage)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.V1.State as V1
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2
import qualified Proto.Bd2.State.V2.State_Fields as V2Fields

-- The caller extracts this ordered, versioned fact from its installed
-- GameData. Neither this module nor the Haskell executable reads the catalog.
data StepUpFact = StepUpFact
  { stepUpGameDataVersion :: Text
  , stepUpGroupId :: Word64
  , orderedGachaIds :: [Word64]
  }

repairStepUpProgress :: StepUpFact -> V2.Snapshot -> Either [Problem] (V2.Snapshot, Word64)
repairStepUpProgress fact snapshot
  | Text.null (stepUpGameDataVersion fact) || stepUpGameDataVersion fact /= snapshot ^. V2Fields.gameDataVersion =
      Left [problem "repair.step_up_game_data_version" "step-up design and snapshot GameData versions differ" []]
  | stepUpGroupId fact == 0 || null (orderedGachaIds fact) || 0 `elem` orderedGachaIds fact =
      Left [problem "repair.step_up_invalid_fact" "ordered step-up design must have nonzero IDs" []]
  | Set.size (Set.fromList (orderedGachaIds fact)) /= length (orderedGachaIds fact) =
      Left [problem "repair.step_up_duplicate_gacha" "step-up design contains duplicate gacha IDs" (orderedGachaIds fact)]
  | otherwise =
      let ledger = snapshot ^. V2Fields.collection
          groupKey = Text.pack (show (stepUpGroupId fact))
          stored = toList (ledger ^. V2Fields.stepUpProgress)
          current = maybe 0 (^. V1Fields.value) (find ((== groupKey) . (^. V1Fields.identity)) stored)
          grantIds = map (^. V1Fields.identity) (toList (ledger ^. V2Fields.grants))
          matches gachaId = any (Text.isPrefixOf ("regular-gacha:" <> Text.pack (show gachaId) <> ":")) grantIds
          derived = fromIntegral (length (takeWhile matches (orderedGachaIds fact)))
          repairedCount = max current derived
          adjust entry
            | entry ^. V1Fields.identity == groupKey = entry & V1Fields.value .~ repairedCount
            | otherwise = entry
          repairedEntries
            | repairedCount == current = stored
            | any ((== groupKey) . (^. V1Fields.identity)) stored = map adjust stored
            | otherwise = stored <> [newCount groupKey repairedCount]
       in if length (filter ((== groupKey) . (^. V1Fields.identity)) stored) > 1
            then Left [problem "repair.step_up_duplicate_progress" "step-up group has multiple progress records" [stepUpGroupId fact]]
            else if current > fromIntegral (length (orderedGachaIds fact))
            then Left [problem "repair.step_up_out_of_range" "stored step count exceeds the ordered design" [current]]
            else Right (snapshot & V2Fields.collection . V2Fields.stepUpProgress .~ repairedEntries, repairedCount)
  where
    problem :: Text -> Text -> [Word64] -> Problem
    problem code = Problem code Error ["collection", "step_up_progress"]

newCount :: Text -> Word64 -> V1.NamedCount
newCount identity count =
  defMessage & V1Fields.identity .~ identity & V1Fields.value .~ count
