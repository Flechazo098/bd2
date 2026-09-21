module BD2.State.Framing
  ( decodeDelimited
  , encodeDelimited
  ) where

import Data.Bits ((.&.), (.|.), shiftL, shiftR)
import qualified Data.ByteString as BS
import Data.ProtoLens (Message, decodeMessage, encodeMessage)
import Data.Word (Word64, Word8)

maxFrameBytes :: Word64
maxFrameBytes = 16 * 1024 * 1024

decodeDelimited :: Message a => BS.ByteString -> Either String a
decodeDelimited input = do
  (size, prefixLength) <- decodeLength input
  if size > maxFrameBytes
    then Left "frame exceeds 16 MiB"
    else do
      let payload = BS.take (fromIntegral size) (BS.drop prefixLength input)
          trailing = BS.drop (prefixLength + fromIntegral size) input
      if BS.length payload /= fromIntegral size
        then Left "truncated protobuf frame"
        else if not (BS.null trailing)
          then Left "trailing bytes after protobuf frame"
          else decodeMessage payload

encodeDelimited :: Message a => a -> BS.ByteString
encodeDelimited value =
  let payload = encodeMessage value
   in BS.pack (encodeLength (fromIntegral (BS.length payload))) <> payload

decodeLength :: BS.ByteString -> Either String (Word64, Int)
decodeLength = go 0 0 0 . BS.unpack
  where
    go :: Word64 -> Int -> Int -> [Word8] -> Either String (Word64, Int)
    go _ _ _ [] = Left "missing frame length"
    go value shift count (byte : rest)
      | count >= 9 = Left "frame length varint is too long"
      | byte .&. 0x80 == 0 = Right (value .|. (fromIntegral byte `shiftL` shift), count + 1)
      | otherwise = go (value .|. (fromIntegral (byte .&. 0x7f) `shiftL` shift)) (shift + 7) (count + 1) rest

encodeLength :: Word64 -> [Word8]
encodeLength value
  | value < 0x80 = [fromIntegral value]
  | otherwise = (fromIntegral (value .&. 0x7f) .|. 0x80) : encodeLength (value `shiftR` 7)
