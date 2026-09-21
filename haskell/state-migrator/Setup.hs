import Data.ProtoLens.Setup (defaultMainGeneratingSpecificProtos)

main :: IO ()
main =
  defaultMainGeneratingSpecificProtos "../../proto" $ \_ ->
    pure
      [ "bd2/state/v1/state.proto"
      , "bd2/state/control/v1/control.proto"
      , "bd2/state/v2/state.proto"
      , "bd2/state/control/v2/control.proto"
      ]
