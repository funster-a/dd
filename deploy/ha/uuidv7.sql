-- Только для стенда отказоустойчивости (ADR 028): образ с repmgr — PostgreSQL
-- 17, а uuidv7() встроена с PostgreSQL 18. Та же функция: 48 бит времени в
-- миллисекундах, версия 7, остальное — случайное.
CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
  SELECT encode(
    set_bit(set_bit(
      overlay(uuid_send(gen_random_uuid())
              PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
              FROM 1 FOR 6),
      52, 1), 53, 1),
    'hex')::uuid
$$;
