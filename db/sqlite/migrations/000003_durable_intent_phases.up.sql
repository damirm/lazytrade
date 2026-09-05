-- Legacy pending did not distinguish an API call which had not started from
-- one interrupted in flight. Preserve safety by treating every such row as
-- having an unknown exchange outcome.
UPDATE order_intents
SET status = 'unknown'
WHERE status = 'pending';
