package server

// LuaTokenBucket is the atomic bucket update script. It runs server-side under
// EVAL/EVALSHA so the read-modify-write of the per-key state is indivisible.
//
// KEYS[1]  bucket key
// ARGV[1]  rate in tokens/sec (also bucket capacity)
// ARGV[2]  requested tokens
//
// Returns the integer count of tokens granted (0..ARGV[2]).
//
// Notes:
//   - Time comes from `redis.call('TIME')` server-side, so callers do not have
//     to agree on a clock and we cannot be tricked by skewed callers.
//   - Uses HSET (HMSET is deprecated in Redis 4.0+).
//   - PEXPIRE refreshes the key TTL on every operation; an idle bucket is
//     evicted after 60s and re-seeded at full capacity by the next caller.
const LuaTokenBucket = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local req  = tonumber(ARGV[2])
local time = redis.call('TIME')
local now = (tonumber(time[1]) * 1000) + math.floor(tonumber(time[2]) / 1000)
local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1]) or rate
local ts     = tonumber(data[2]) or now
tokens = math.max(0, math.min(rate, tokens + (now - ts) * rate / 1000))
local granted = math.min(req, tokens)
tokens = tokens - granted
redis.call('HSET', key, 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', key, 60000)
return math.floor(granted)
`
