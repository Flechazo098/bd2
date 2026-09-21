{-# LANGUAGE DeriveGeneric #-}

module BD2.State.Domain
  ( InventoryIndex (..)
  , PackId (..)
  , QuestId (..)
  , QuestKey (..)
  , ValidSnapshot (..)
  , CharacterRedirect (..)
  , Problem (..)
  , Severity (..)
  ) where

import Data.Text (Text)
import Data.Word (Word32, Word64)
import GHC.Generics (Generic)
import qualified Proto.Bd2.State.V1.State as State

newtype InventoryIndex = InventoryIndex Word64
  deriving stock (Eq, Ord, Show, Generic)

newtype PackId = PackId Word32
  deriving stock (Eq, Ord, Show, Generic)

newtype QuestId = QuestId Word32
  deriving stock (Eq, Ord, Show, Generic)

data QuestKey = QuestKey
  { packId :: PackId
  , questId :: QuestId
  }
  deriving stock (Eq, Ord, Show, Generic)

-- Validated semantic view used by migrations. The original DTO is retained
-- for lossless pass-through, while ownership is no longer inferred again by
-- each migration from two unrelated protobuf containers.
data ValidSnapshot = ValidSnapshot
  { validatedSource :: State.Snapshot
  , validatedBaseCharacters :: [State.Character]
  , validatedAcquiredCharacters :: [State.Character]
  , validatedCostumes :: [State.Costume]
  }

data CharacterRedirect = CharacterRedirect
  { redirectCharacterId :: Word64
  , redirectFromIndex :: Word64
  , redirectToIndex :: Word64
  }
  deriving stock (Eq, Ord, Show, Generic)

data Severity = Warning | Error
  deriving stock (Eq, Ord, Show, Generic)

data Problem = Problem
  { problemCode :: Text
  , problemSeverity :: Severity
  , problemPath :: [Text]
  , problemMessage :: Text
  , problemRelatedIds :: [Word64]
  }
  deriving stock (Eq, Show, Generic)
