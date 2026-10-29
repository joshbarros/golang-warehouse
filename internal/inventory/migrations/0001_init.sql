CREATE TABLE inventory.skus (
    id          uuid PRIMARY KEY,
    code        text        NOT NULL UNIQUE CHECK (char_length(code) <= 20),
    name     text        NOT NULL,
    category     text        NOT NULL,
    bin     text        NOT NULL,
    received_at     timestamptz NOT NULL,
    received_units integer     NOT NULL,
    on_hand_units  integer     NOT NULL,
    created_at  timestamptz NOT NULL,
    CONSTRAINT skus_remaining CHECK (on_hand_units >= 0 AND on_hand_units <= received_units)
);

CREATE TABLE inventory.unit_holds (
    order_id       uuid PRIMARY KEY,
    owner_id       uuid        NOT NULL,
    sku_id        uuid        NOT NULL REFERENCES inventory.skus (id),
    units          integer     NOT NULL,
    status         text        NOT NULL,
    created_at     timestamptz NOT NULL,
    released_at    timestamptz,
    release_reason text
);

CREATE TABLE inventory.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX skus_outbox_unsent ON inventory.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE inventory.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);
