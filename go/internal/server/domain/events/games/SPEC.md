# Seasonal minigame rules

The registry supplies event identity and dates. This package never creates,
extends or activates a season. Each schedule UID owns independent progress.
Queries initialize a valid new board and return persisted progress. Mutations
require a live matching schedule. Requests are identified by session, path and
sequence; identical retries replay the entire stored response and changed
retries fail.

GameData supplies costs, move ranges, board size, reward sets, completion
thresholds, free roulette draws, special slots and pity thresholds. Client
protobuf and packet callbacks supply field shapes. RouletteInfo capture is a
comparison sample, not runtime data.

LocalText 460038 describes Bingo opening rewards and completed-line rewards.
LocalText 460040 describes puzzle tile rewards, word completion rewards and
the final stage's prohibition on renewal. These meanings drive the following
server rules:

- Dice starts at the first designed scaffold. Each configured controller
  samples uniformly inside its inclusive range. Crossing the end advances
  completion count; all newly crossed completion rewards and the final
  landing scaffold reward settle together.
- Bingo shuffles the reward IDs in its current clear-count reward set.
  Opening samples unopened zero-based positions without replacement.
  Protocol line types are diagonal 0, row 1 and column 2. Each line pays once
  per board. Finishing advances count and creates a fresh board; after the
  last designed reward tier, that tier repeats.
- Puzzle starts at designed stage 1. Reward IDs form the board. Opening a
  tile pays its reward; opening all members of a word pays that word once.
  Renewal requires all current-stage word members, as the client checks,
  and advances only while another designed stage exists. Unopened incidental
  tiles may be abandoned on renewal. The final stage cannot renew.
- Roulette free draws reset at UTC midnight (KST 09:00); accumulated tries
  and special/pity state survive daily reset. Free and paid draws enforce
  their separate costs. Weighted sampling uses the design's primary weights,
  then secondary weights after a special reward where provided. Pity forces
  a designed special slot. Accumulated threshold rewards pay when crossed.

Randomness uses crypto/rand and can be injected in package tests. All submitted
consumed item records must match the server-computed design cost. State is
cloned, an entire batch is simulated and validated, and then economic rewards
and the snapshot are saved. Production must run this handler under the
account repository's request transaction; failed requests roll back item,
wallet and snapshot changes together. The main server passes an entry-backed
snapshot store, so no existing domain core layout changes.
