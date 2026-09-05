package redis

// Schedule payloads remain opaque. Identity/counter/lease metadata must be
// valid before a transition can acknowledge, advance or reserve due work.
const scheduleMetadataLua = `
local function validateScheduleKeys(kinds)
 for i,key in ipairs(KEYS) do local kind=redis.call('TYPE',key).ok;if kind~='none' and kind~=kinds[i] then error('invalid schedule key type') end end
end
local function validScheduleCounter(value)
 return type(value)=='string' and string.match(value,'^%d+$') and (#value==1 or string.sub(value,1,1)~='0') and (#value<20 or (#value==20 and value<='18446744073709551615'))
end
local function scheduleMillis(value)
 local number=tonumber(value)
 if not number or number~=math.floor(number) or math.abs(number)>9007199254740991 then error('invalid schedule milliseconds') end
 return number
end
local function readSchedule(raw,id,periodic)
 local item=cjson.decode(raw)
 if type(item)~='table' or item.format~=1 or item.id~=id or type(item.payload)~='string' or #item.payload==0 or not validScheduleCounter(item.revision) or not validScheduleCounter(item.fence) or type(item.owner)~='string' or type(item.lease_millis)~='number' or item.lease_millis<0 then error('invalid schedule metadata') end
 scheduleMillis(item.lease_millis)
 if periodic then
  if type(item.enabled)~='boolean' then error('invalid periodic state') end
 elseif type(item.digest)~='string' or #item.digest~=64 then error('invalid delayed digest') end
 return item
end
`
