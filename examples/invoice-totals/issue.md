# Invoice summary drops the last line item

Since we switched billing to the new exporter (v3) the invoice summary script
seems to skip the final line of each invoice. For March, INV-2031 has 6 lines
but the summary showed `lines: 5` and `total: 1180.00`. The Priority support
line (49.50) was missing and our accountant only caught it during month-end
close. Expected total is 1,229.50.

My guess is the missing newline at the end of the file. The new exporter
doesn't write one and the old one did.

Attached are the exact export we ran it on (`invoice-2031.csv`, straight from
the exporter, nobody opened it in Excel) and the summary script we run. We run
it like this from the folder with both files:

    python3 summarize.py invoice-2031.csv

Python 3.12 on the finance box. This is holding up month-end so any help is
appreciated. Can you confirm the newline thing and tell us what to change?
