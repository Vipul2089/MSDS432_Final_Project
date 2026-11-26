# Exploratory data analysis

Jupyter notebooks that profile the input datasets. The findings are written up
in [`docs/eda/eda-report.md`](../docs/eda/eda-report.md).

Python is used here for analysis only; all system services are written in Go
(NFR-01).

## Contents

| File | Purpose |
| --- | --- |
| [`01_nslkdd_eda.ipynb`](01_nslkdd_eda.ipynb) | Profiles `KDDTrain+.txt` and `KDDTest+.txt`: quality, class balance, protocols and flags, byte distributions, train versus test |
| [`02_cloudtrail_eda.ipynb`](02_cloudtrail_eda.ipynb) | Profiles the 20 flaws.cloud CloudTrail files: completeness, duplicates, time coverage, actions, outcomes, identities, record size |
| [`download_data.sh`](download_data.sh) | Downloads both datasets into `data/raw/` and writes `data/raw/SHA256SUMS` |
| [`requirements.txt`](requirements.txt) | Python dependencies: pandas, matplotlib, ipykernel, nbconvert |

## Usage

Run from the repository root:

```sh
make eda-setup   # create eda/.venv and install dependencies
make eda-data    # download the raw datasets (about 270 MB)
make eda         # run both notebooks top to bottom and save their outputs
```

To work interactively, open a notebook in VS Code or Jupyter, select the
`eda/.venv` Python interpreter as the kernel, and run all cells. The CloudTrail
notebook takes about 2 minutes and about 3 GB of memory.

Each notebook writes:

- Figures to `docs/eda/figures/` (used by the EDA report)
- Numbers to `docs/eda/results/*_summary.json` (quoted by the EDA report)

## Conventions

- Notebooks are numbered (`01_`, `02_`) in the order they should be read.
- Each notebook starts with a purpose and inputs/outputs table and ends with a
  findings section.
- All numbers come from one `profile()` function per notebook, so tables,
  charts, and the saved summary always agree.
- Before committing, restart the kernel and run all cells (or run `make eda`),
  so the saved outputs match the code. Outputs are committed so the notebooks
  can be read without running them.
- Raw data (`data/`), the virtual environment (`eda/.venv/`), and
  `.ipynb_checkpoints/` are not committed.
