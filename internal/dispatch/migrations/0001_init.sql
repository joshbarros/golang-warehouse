CREATE TABLE dispatch.slips (
    order_id   uuid PRIMARY KEY,
    owner_id   uuid        NOT NULL,
    number     text,
    status     text        NOT NULL,
    reason     text,
    created_at timestamptz NOT NULL
);

CREATE TABLE dispatch.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX slips_outbox_unsent ON dispatch.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE dispatch.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);

