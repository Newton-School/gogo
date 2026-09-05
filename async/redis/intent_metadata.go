package redis

// The opaque payload is never interpreted by Lua. Validate every metadata value
// used for routing, ownership, counters or arithmetic before any Redis write;
// stage the complete encoded replacement before updating either key.
const intentMetadataLua = `
local kinds={'hash','zset'}
for i,key in ipairs(KEYS) do local kind=redis.call('TYPE',key).ok;if kind~='none' and kind~=kinds[i] then error('invalid intent key type') end end
local function validCounter(value)
 return type(value)=='string' and string.match(value,'^%d+$') and (#value==1 or string.sub(value,1,1)~='0') and (#value<20 or (#value==20 and value<='18446744073709551615'))
end
local function readIntent(raw,id,source)
 local item=cjson.decode(raw)
 if type(item)~='table' or item.format~=1 or item.id~=id or item.source_id~=source or type(item.payload)~='string' or #item.payload==0 or not validCounter(item.fence) or not validCounter(item.revision) or type(item.owner)~='string' or type(item.delivered)~='boolean' or type(item.lease_millis)~='number' or item.lease_millis<0 or item.lease_millis>9007199254740991 or item.lease_millis~=math.floor(item.lease_millis) then error('invalid intent metadata') end
 return item
end
`
