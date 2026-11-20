-- Real excerpt of the Senat dosleg PostgreSQL dump (data.senat.fr/data/dosleg/dosleg.zip).
-- Rows are verbatim captures; a coherent subset for scrutin (2006, 42) is kept, plus two
-- votsen rows whose senator is absent from this auteur excerpt (matricule-fallback path).

COPY posvot (posvotcod, posvotlib) FROM stdin;
1	pour
2	contre
3	abstention
4	non-votant
\.

COPY auteur (autcod, quacod, typautcod, nomuse, prenom, nomtec, autmat, grpapp, grprat, autfct, datdeb, datfin, senfem) FROM stdin;
98046X	1 	1  	MARC	François	MARC_FRANCOIS	98046X	SOC	N	\N	\N	\N	NON
98047Y	1 	1  	FRÉVILLE	Yves	FREVILLE	98047Y	UC-UDF	N	\N	\N	\N	NON
98048A	1 	1  	de BROISSIA	Louis	BROISSIA (DE)	98048A	UMP	N	\N	\N	\N	NON
98049B	2 	1  	BOYER	Yolande	BOYER YOLANDE	98049B	SOC	N	\N	\N	\N	NON
98050T	1 	1  	PONIATOWSKI	Ladislas	PONIATOWSKI	98050T	UMP	N	\N	\N	\N	NON
98051U	1 	1  	LE PENSEC	Louis	LEPENSEC	98051U	SOC	N	\N	\N	\N	NON
98052V	1 	1  	FLOSSE	Gaston	FLOSSE	98052V	UMP	N	\N	\N	\N	NON
\.

COPY scr (sesann, scrnum, code, scrint, scrdat, scrpou, scrcon, scrvot, scrsuf, scrvotsea, scrsufsea, scrpousea, scrconsea, scrmaj, scrmajsea, soslib, scrbaspag, scrdateff, scrjso) FROM stdin;
2006	42	10693	sur l'ensemble du projet de loi relatif au secteur de l'énergie dans la rédaction du texte proposé par la commission mixte paritaire.	2006-11-08 00:00:00	170	138	327	308	327	308	170	138	155	155	\N	\N	\N	I
\.

COPY votsen (sesann, scrnum, senmat, posvotcod, titsencod, stavotidt, senmatdel, votsenmar) FROM stdin;
2006	42	98046X	2	0	0	\N	\N
2006	42	98047Y	1	0	0	\N	\N
2006	42	98048A	1	0	0	\N	\N
2006	42	98049B	2	0	0	\N	\N
2006	42	98050T	1	0	0	\N	\N
2006	42	98051U	2	0	0	\N	\N
2006	42	98052V	1	0	0	\N	\N
2006	42	98053W	2	0	0	\N	\N
2006	42	92044U	3	0	0	\N	\N
\.
