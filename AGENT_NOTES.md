# Agent notes

I used Cursor's coding agent for the schema, the Go services, the tests, and the sync page. I set up the Neon project and checked the connection string myself.

What the agent got wrong: it showed a sample `DATABASE_URL` with `...` standing in for the password. I pasted that sample into `.env`, so the password was three dots and Neon rejected the connection. The real password is the one copied from the Neon connect box, not the sample.

What I caught: the Neon connect box starts with connection pooling turned on. That host name contains `-pooler`. This app locks an issue row for the whole receipt transaction, so it needs the direct connection. I turned pooling off before copying the string.
