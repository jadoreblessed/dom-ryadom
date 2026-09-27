INSERT INTO managing_orgs (id, name, phone, emergency_phone) VALUES
    ('uk-1', 'Тестовая УК №1', '+7 900 000-00-01', '+7 900 000-00-11'),
    ('uk-2', 'Тестовая УК №2', '+7 900 000-00-02', '+7 900 000-00-12');

INSERT INTO buildings (id, address, managing_org_id) VALUES
    ('house-1', 'ул. Лесная, д. 5', 'uk-1'),
    ('house-2', 'ул. Садовая, д. 10', 'uk-2');
