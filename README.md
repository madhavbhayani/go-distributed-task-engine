# DataForge — Distributed ETL Pipeline Engine

> A learning project by **Madhav Bhayani** — exploring distributed systems, Go concurrency patterns, and real-world ETL pipeline design.

---

## What Is DataForge?

DataForge is a full-stack ETL (Extract, Transform, Load) pipeline engine built entirely from scratch. It lets you upload CSV files, run multi-stage data transformations — cleaning, normalization, and deduplication — and export clean datasets, all powered by a concurrent worker pool and a typed REST API.

Every component is hand-built without relying on external Go frameworks, as a deliberate exercise in systems programming.

## Architecture

```
┌────────────────────────────────────────────────────────┐
│  React 19 + Tailwind v4   (frontend)                   │
│  Upload → Analyze → Clean → Normalize → Dedup → Export │
└─────────────────────────┬──────────────────────────────┘
                          │ REST API
┌─────────────────────────┴──────────────────────────────┐
│  Go 1.25 Backend (chi router)                          │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐        │
│  │ Dispatcher│→ │ Worker   │→ │ ETL Executors │        │
│  │          │  │ Pool (N) │  │ (clean/norm/  │        │
│  │          │  │          │  │  dedup)       │        │
│  └──────────┘  └──────────┘  └───────────────┘        │
│  ┌──────────────────┐  ┌───────────────────┐          │
│  │ Priority Queue    │  │ In-Memory Stores  │          │
│  │ (high/med/low)    │  │ (jobs, datasets)  │          │
│  └──────────────────┘  └───────────────────┘          │
└────────────────────────────────────────────────────────┘
```

## Tech Stack

| Layer     | Technology                          |
| --------- | ----------------------------------- |
| Backend   | Go 1.25, chi router, in-memory stores |
| Frontend  | React 19, Vite 7, Tailwind CSS v4   |
| Analytics | Firebase Analytics + Firestore      |
| License   | MIT                                 |

## Key Features

- **Concurrent Worker Pool** — configurable goroutine pool with priority dispatching
- **Intelligent CSV Analyzer** — automatic column type detection with 85% majority-vote threshold
- **Multi-Stage ETL Pipeline** — clean → normalize → deduplicate with detailed per-step reports
- **Smart Cleaning** — null filling, whitespace trimming, type coercion with per-cell change tracking
- **Exact & Fuzzy Dedup** — configurable match columns, keep strategies, and detailed group reports with Load More pagination
- **Dry Run Mode** — preview duplicates without modifying data
- **Typed REST API** — structured JSON responses with health checks
- **React Dashboard** — real-time pipeline stepper with quality delta tracking

## Getting Started

### Prerequisites

- Go 1.25+
- Node.js 20+

### Run the backend

```bash
cd "Go Distributed Job Processing Unit Project"
go run cmd/server/main.go
```

The API server starts on `http://localhost:8080`.

### Run the frontend

```bash
cd frontend/go-distributed-ui
npm install
npm run dev
```

Opens at `http://localhost:5173`.

## Project Structure

```
├── cmd/server/          # Entry point
├── internal/
│   ├── analyzer/        # CSV column type detection
│   ├── api/             # REST handlers & router
│   ├── config/          # Server configuration
│   ├── dataset/         # Dataset types & storage
│   ├── dispatcher/      # Job dispatcher
│   ├── executor/        # ETL executors (clean, normalize, dedup)
│   ├── models/          # Shared data models
│   ├── monitor/         # Health & metrics
│   ├── queue/           # Priority job queue
│   ├── store/           # In-memory job store
│   ├── validator/       # Input validation
│   └── worker/          # Concurrent worker pool
├── frontend/
│   └── go-distributed-ui/  # React + Vite app
└── README.md
```

## License

MIT — see [LICENSE](LICENSE) for details.

---

*Built as a learning exercise in distributed systems and Go concurrency.  
Star the repo if you find it interesting!*